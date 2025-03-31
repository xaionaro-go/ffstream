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
	"github.com/xaionaro-go/ffstream/pkg/ffstream/types"
	"github.com/xaionaro-go/observability"
)

type FFStream struct {
	Switch         *kernel.Switch[kernel.Abstract]
	Recoder        *kernel.Recoder[*codec.NaiveDecoderFactory, *codec.NaiveEncoderFactory]
	Input          *kernel.Input
	Output         *kernel.Output
	FilterThrottle *condition.VideoAverageBitrateLower

	RecodingConfig types.RecoderConfig

	nodeInput   *avpipeline.Node[*processor.FromKernel[*kernel.Input]]
	nodeRecoder *avpipeline.Node[*processor.FromKernel[*kernel.Switch[kernel.Abstract]]]
	nodeOutput  *avpipeline.Node[*processor.FromKernel[*kernel.Output]]

	locker    sync.Mutex
	waitGroup sync.WaitGroup
}

/*
//              +----> COPY ->--+      THROTTLE
// INPUT -> SWITCH              +---------------------> OUTPUT
//              +--> RECODER ->-+
*/
func New(ctx context.Context) *FFStream {
	return &FFStream{
		FilterThrottle: condition.NewVideoAverageBitrateLower(ctx, 0, 0),
	}
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
) types.RecoderConfig {
	s.locker.Lock()
	defer s.locker.Unlock()
	return s.RecodingConfig
}

func (s *FFStream) SetRecoderConfig(
	ctx context.Context,
	cfg types.RecoderConfig,
) error {
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
		codec.NewNaiveDecoderFactory(ctx, cfg.Video.HardwareDeviceType, cfg.Video.HardwareDeviceName, nil),
		codec.NewNaiveEncoderFactory(ctx, cfg.Video.CodecName, "copy", cfg.Video.HardwareDeviceType, cfg.Video.HardwareDeviceName, nil),
		nil,
	)
	if err != nil {
		return fmt.Errorf("unable to initialize a recoder: %w", err)
	}

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

	assertHealthyNodeRecoder(ctx, s.nodeRecoder)
	err := s.nodeRecoder.Processor.Kernel.SetKernelIndex(ctx, 1)
	if err != nil {
		return fmt.Errorf("unable to switch to recoding: %w", err)
	}
	s.FilterThrottle.AverageBitRate = 0 // disable the throttler

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
	err := s.nodeRecoder.Processor.Kernel.SetKernelIndex(ctx, 0)
	if err != nil {
		return fmt.Errorf("unable to switch to passthrough: %w", err)
	}
	s.FilterThrottle.BitrateAveragingPeriod = cfg.Video.AveragingPeriod
	s.FilterThrottle.AverageBitRate = cfg.Video.AverageBitRate // if AverageBitRate != 0 then here we also enable the throttler (if it was disabled)
	return nil
}

func (s *FFStream) GetStats(
	ctx context.Context,
) *avpipeline.NodeStatistics {
	result := &avpipeline.NodeStatistics{}
	result.BytesCountRead.Store(s.nodeInput.NodeStatistics.BytesCountWrote.Load())
	result.BytesCountWrote.Store(s.nodeOutput.NodeStatistics.BytesCountRead.Load())

	inputStats := &s.nodeInput.NodeStatistics.FramesWrote
	result.FramesRead.Unknown.Store(inputStats.Unknown.Load())
	result.FramesRead.Other.Store(inputStats.Other.Load())
	result.FramesRead.Video.Store(inputStats.Video.Load())
	result.FramesRead.Audio.Store(inputStats.Audio.Load())

	outputStats := &s.nodeOutput.NodeStatistics.FramesRead
	result.FramesWrote.Unknown.Store(outputStats.Unknown.Load())
	result.FramesWrote.Other.Store(outputStats.Other.Load())
	result.FramesWrote.Video.Store(outputStats.Video.Load())
	result.FramesWrote.Audio.Store(outputStats.Audio.Load())
	return result
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
		kernel.NewSwitch[kernel.Abstract](s.Recoder, kernel.Passthrough{}),
		processor.DefaultOptionsRecoder()...,
	)
	s.nodeRecoder.Processor.Kernel.SetVerifySwitchOutput(condition.And{
		condition.MediaType(astiav.MediaTypeVideo),
		condition.IsKeyFrame(true),
	})
	s.nodeOutput = avpipeline.NewNodeFromKernel(
		ctx,
		s.Output,
		processor.DefaultOptionsOutput()...,
	)
	s.nodeOutput.InputPacketCondition = s.FilterThrottle

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
	s.waitGroup.Add(1)
	observability.Go(ctx, func() {
		defer s.waitGroup.Done()
		defer cancelFn()
		defer logger.Debugf(ctx, "finished the serving routine")
		avpipeline.ServeRecursively(ctx, s.nodeInput, avpipeline.ServeConfig{}, errCh)
	})

	return nil
}

func (s *FFStream) Wait(
	ctx context.Context,
) error {
	s.waitGroup.Wait()
	return nil
}
