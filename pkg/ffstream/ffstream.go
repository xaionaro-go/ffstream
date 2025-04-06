package ffstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/asticode/go-astiav"
	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline"
	"github.com/xaionaro-go/avpipeline/codec"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/packet/condition"
	"github.com/xaionaro-go/avpipeline/processor"
	"github.com/xaionaro-go/avpipeline/quality"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/observability"
	"github.com/xaionaro-go/xsync"
)

type FFStream struct {
	Input            *kernel.Input
	Switch           *kernel.Switch[kernel.Abstract]
	FilterThrottle   *condition.VideoAverageBitrateLower
	Recoder          *kernel.Recoder[*codec.NaiveDecoderFactory, *codec.NaiveEncoderFactory]
	MapStreamIndices *kernel.MapStreamIndices
	Output           *kernel.Output

	RecodingConfig types.RecoderConfig

	nodeInput   *avpipeline.Node[*processor.FromKernel[*kernel.Input]]
	nodeRecoder *avpipeline.Node[*processor.FromKernel[*kernel.Switch[kernel.Abstract]]]
	nodeOutput  *avpipeline.Node[*processor.FromKernel[*kernel.Output]]

	locker    sync.Mutex
	waitGroup sync.WaitGroup
}

/*
//              +----> COPY ----> THROTTLE ->---+
// INPUT -> SWITCH                              +--> MAP INDICES --> OUTPUT
//              +---------> RECODER -->---------+
*/
func New(ctx context.Context) *FFStream {
	s := &FFStream{
		FilterThrottle: condition.NewVideoAverageBitrateLower(ctx, 0, 0),
	}
	s.MapStreamIndices = kernel.NewMapStreamIndices(ctx, newStreamIndexAssigner(s))
	return s
}

func (s *FFStream) AddInput(
	ctx context.Context,
	input *kernel.Input,
) error {
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.Input != nil {
		return fmt.Errorf("currently we support only one input")
	}
	s.Input = input
	return nil
}

func (s *FFStream) AddOutput(
	ctx context.Context,
	output *kernel.Output,
) error {
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.Output != nil {
		return fmt.Errorf("currently we support only one output")
	}
	s.Output = output
	return nil
}

func (s *FFStream) GetRecoderConfig(
	ctx context.Context,
) (_ret types.RecoderConfig) {
	logger.Tracef(ctx, "GetRecoderConfig")
	defer func() { logger.Tracef(ctx, "/GetRecoderConfig: %v", _ret) }()
	s.locker.Lock()
	defer s.locker.Unlock()
	encoderKernelIndex := s.Switch.GetKernelIndex(ctx)
	logger.Tracef(ctx, "encoderKernelIndex: %v", encoderKernelIndex)
	if encoderKernelIndex == 0 {
		return s.RecodingConfig
	}
	cpy := s.RecodingConfig
	cpy.Video.CodecName = codec.CodecNameCopy
	return cpy
}

func (s *FFStream) SetRecoderConfig(
	ctx context.Context,
	cfg types.RecoderConfig,
) (_err error) {
	logger.Tracef(ctx, "SetRecoderConfig(ctx, %#+v)", cfg)
	defer func() { logger.Tracef(ctx, "/SetRecoderConfig(ctx, %#+v): %v", cfg, _err) }()
	s.locker.Lock()
	defer s.locker.Unlock()
	err := s.configureRecoder(ctx, cfg)
	if err != nil {
		return fmt.Errorf("unable to reconfigure the recoder: %w", err)
	}
	s.RecodingConfig = cfg
	return nil
}

func (s *FFStream) configureRecoder(
	ctx context.Context,
	cfg types.RecoderConfig,
) error {
	if s.Recoder == nil {
		if err := s.initRecoder(ctx, cfg); err != nil {
			return fmt.Errorf("unable to initialize the recoder: %w", err)
		}
		return nil
	}
	if cfg.Audio.CodecName != "copy" {
		return fmt.Errorf("we currently do not support audio recoding: '%s' != 'copy'", cfg.Audio.CodecName)
	}
	if cfg.Video.CodecName == "copy" {
		if err := s.reconfigureRecoderCopy(ctx, cfg); err != nil {
			return fmt.Errorf("unable to reconfigure to copying: %w", err)
		}
		return nil
	}
	if err := s.reconfigureRecoder(ctx, cfg); err != nil {
		return fmt.Errorf("unable to reconfigure the recoder: %w", err)
	}
	return nil
}

func (s *FFStream) initRecoder(
	ctx context.Context,
	cfg types.RecoderConfig,
) error {
	if s.Recoder != nil {
		return fmt.Errorf("internal error: an encoder is already initialized")
	}

	var err error
	s.Recoder, err = kernel.NewRecoder(
		ctx,
		codec.NewNaiveDecoderFactory(ctx,
			avptypes.HardwareDeviceType(cfg.Video.HardwareDeviceType),
			avptypes.HardwareDeviceName(cfg.Video.HardwareDeviceName),
			nil,
			nil,
		),
		codec.NewNaiveEncoderFactory(ctx,
			cfg.Video.CodecName,
			"copy",
			avptypes.HardwareDeviceType(cfg.Video.HardwareDeviceType),
			avptypes.HardwareDeviceName(cfg.Video.HardwareDeviceName),
			convertCustomOptions(cfg.Video.CustomOptions),
			convertCustomOptions(cfg.Audio.CustomOptions),
		),
		nil,
	)
	if err != nil {
		return fmt.Errorf("unable to initialize a recoder: %w", err)
	}

	s.Switch = kernel.NewSwitch[kernel.Abstract](
		s.Recoder,
		kernel.NewSequence[kernel.Abstract](
			kernel.NewFilter(s.FilterThrottle, nil),
			kernel.NewMapStreamIndices(ctx, nil),
		),
	)
	s.Switch.SetVerifySwitchOutput(condition.And{
		condition.MediaType(astiav.MediaTypeVideo),
		condition.IsKeyFrame(true),
	})
	return nil
}

func (s *FFStream) reconfigureRecoder(
	ctx context.Context,
	cfg types.RecoderConfig,
) error {
	encoderFactory := s.Recoder.EncoderFactory
	if cfg.Video.CodecName != encoderFactory.VideoCodec {
		return fmt.Errorf("unable to change the encoding codec on the fly, yet: '%s' != '%s'", cfg.Video.CodecName, encoderFactory.VideoCodec)
	}

	err := xsync.DoR1(ctx, &s.Recoder.EncoderFactory.Locker, func() error {
		if len(s.Recoder.EncoderFactory.VideoEncoders) == 0 {
			logger.Debugf(ctx, "the encoder is not yet initialized, so asking it to have the correct settings when it will be being initialized")

			if s.Recoder.EncoderFactory.VideoOptions == nil {
				s.Recoder.EncoderFactory.VideoOptions = astiav.NewDictionary()
				setFinalizerFree(ctx, s.Recoder.EncoderFactory.VideoOptions)
			}

			if cfg.Video.AverageBitRate == 0 {
				s.Recoder.EncoderFactory.VideoOptions.Unset("b")
			} else {
				s.Recoder.EncoderFactory.VideoOptions.Set("b", fmt.Sprintf("%d", cfg.Video.AverageBitRate), 0)
			}
			return nil
		}

		logger.Debugf(ctx, "the encoder is already initialized, so modifying it if needed")
		encoder := s.Recoder.EncoderFactory.VideoEncoders[0]

		q := encoder.GetQuality(ctx)
		if q == nil {
			logger.Errorf(ctx, "unable to get the current encoding quality")
			q = quality.ConstantBitrate(0)
		}

		needsChangingBitrate := true
		if q, ok := q.(quality.ConstantBitrate); ok {
			if q == quality.ConstantBitrate(cfg.Video.AverageBitRate) {
				needsChangingBitrate = false
			}
		}

		if needsChangingBitrate {
			err := encoder.SetQuality(ctx, quality.ConstantBitrate(cfg.Video.AverageBitRate), nil)
			if err != nil {
				return fmt.Errorf("unable to set bitrate to %v: %w", cfg.Video.AverageBitRate, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	assertHealthyNodeRecoder(ctx, s.nodeRecoder)
	err = s.nodeRecoder.Processor.Kernel.SetKernelIndex(ctx, 0)
	if err != nil {
		return fmt.Errorf("unable to switch to recoding: %w", err)
	}

	return nil
}

func assertHealthyNodeRecoder(
	ctx context.Context,
	nodeRecoder *avpipeline.Node[*processor.FromKernel[*kernel.Switch[kernel.Abstract]]],
) {
	assert(ctx, nodeRecoder != nil, "nodeRecoder != nil")
	assert(ctx, nodeRecoder.Processor != nil, "Processor != nil")
	assert(ctx, nodeRecoder.Processor.Kernel != nil, "Kernel != nil")
}

func (s *FFStream) reconfigureRecoderCopy(
	ctx context.Context,
	cfg types.RecoderConfig,
) error {
	assertHealthyNodeRecoder(ctx, s.nodeRecoder)
	err := s.nodeRecoder.Processor.Kernel.SetKernelIndex(ctx, 1)
	if err != nil {
		return fmt.Errorf("unable to switch to passthrough: %w", err)
	}
	s.FilterThrottle.BitrateAveragingPeriod = cfg.Video.AveragingPeriod
	s.FilterThrottle.AverageBitRate = cfg.Video.AverageBitRate // if AverageBitRate != 0 then here we also enable the throttler (if it was disabled)
	return nil
}

func (s *FFStream) GetStats(
	ctx context.Context,
) *ffstream_grpc.GetStatsReply {
	return &ffstream_grpc.GetStatsReply{
		BytesCountRead:  s.nodeInput.NodeStatistics.BytesCountWrote.Load(),
		BytesCountWrote: s.nodeOutput.NodeStatistics.BytesCountRead.Load(),
		FramesRead: &ffstream_grpc.CommonsProcessingFramesStatistics{
			Unknown: s.nodeInput.NodeStatistics.FramesWrote.Unknown.Load(),
			Other:   s.nodeInput.NodeStatistics.FramesWrote.Other.Load(),
			Video:   s.nodeInput.NodeStatistics.FramesWrote.Video.Load(),
			Audio:   s.nodeInput.NodeStatistics.FramesWrote.Audio.Load(),
		},
		FramesMissed: &ffstream_grpc.CommonsProcessingFramesStatistics{
			Unknown: s.nodeRecoder.NodeStatistics.FramesMissed.Unknown.Load(),
			Other:   s.nodeRecoder.NodeStatistics.FramesMissed.Other.Load(),
			Video:   s.nodeRecoder.NodeStatistics.FramesMissed.Video.Load(),
			Audio:   s.nodeRecoder.NodeStatistics.FramesMissed.Audio.Load(),
		},
		FramesWrote: &ffstream_grpc.CommonsProcessingFramesStatistics{
			Unknown: s.nodeOutput.NodeStatistics.FramesRead.Unknown.Load(),
			Other:   s.nodeOutput.NodeStatistics.FramesRead.Other.Load(),
			Video:   s.nodeOutput.NodeStatistics.FramesRead.Video.Load(),
			Audio:   s.nodeOutput.NodeStatistics.FramesRead.Audio.Load(),
		},
	}
}

func (s *FFStream) Start(
	ctx context.Context,
) error {
	if s.Recoder == nil {
		return fmt.Errorf("Recoder is not configured")
	}
	s.nodeInput = avpipeline.NewNodeFromKernel(
		ctx,
		s.Input,
		processor.DefaultOptionsInput()...,
	)
	s.nodeRecoder = avpipeline.NewNodeFromKernel(
		ctx,
		s.Switch,
		processor.DefaultOptionsRecoder()...,
	)
	s.nodeOutput = avpipeline.NewNodeFromKernel(
		ctx,
		s.Output,
		processor.DefaultOptionsOutput()...,
	)

	s.nodeInput.PushPacketsTo.Add(s.nodeRecoder)
	s.nodeRecoder.PushPacketsTo.Add(s.nodeOutput)

	ctx, cancelFn := context.WithCancel(ctx)

	errCh := make(chan avpipeline.ErrNode, 10)
	s.waitGroup.Add(1)
	observability.Go(ctx, func() {
		defer s.waitGroup.Done()
		defer cancelFn()
		defer logger.Debugf(ctx, "finished the error listening loop")
		for {
			select {
			case err := <-ctx.Done():
				logger.Debugf(ctx, "stopping listening for errors: %v", err)
				return
			case err, ok := <-errCh:
				if !ok {
					logger.Debugf(ctx, "the error channel is closed")
					return
				}

				if errors.Is(err.Err, context.Canceled) {
					logger.Debugf(ctx, "cancelled: %#+v", err)
					continue
				}
				if errors.Is(err.Err, io.EOF) {
					logger.Debugf(ctx, "EOF: %#+v", err)
					continue
				}
				logger.Errorf(ctx, "stopping because received error: %v", err)
				return
			}
		}
	})

	err := avpipeline.NotifyAboutPacketSourcesRecursively(ctx, nil, s.nodeInput)
	if err != nil {
		return fmt.Errorf("receive an error while notifying nodes about packet sources: %w", err)
	}

	s.waitGroup.Add(1)
	observability.Go(ctx, func() {
		defer s.waitGroup.Done()
		defer cancelFn()
		defer logger.Debugf(ctx, "finished the serving routine")
		avpipeline.ServeRecursively(ctx, avpipeline.ServeConfig{}, errCh, s.nodeInput)
	})

	return nil
}

func (s *FFStream) Wait(
	ctx context.Context,
) error {
	s.waitGroup.Wait()
	return nil
}
