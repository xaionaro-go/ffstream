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
	"github.com/xaionaro-go/avpipeline/kernel/bitstreamfilter"
	"github.com/xaionaro-go/avpipeline/packet"
	"github.com/xaionaro-go/avpipeline/packet/condition"
	"github.com/xaionaro-go/avpipeline/packet/filter"
	"github.com/xaionaro-go/avpipeline/processor"
	"github.com/xaionaro-go/avpipeline/quality"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/observability"
	"github.com/xaionaro-go/xsync"
)

const (
	switchOnlyOnKeyFrames      = false
	notifyAboutPacketSources   = true
	startWithPassthrough       = false
	autoInsertBitstreamFilters = true
	passthroughSupport         = true
)

type FFStream struct {
	Input             *kernel.Input
	FilterThrottle    *condition.VideoAverageBitrateLower
	PassthroughSwitch *condition.Switch
	BothPipesSwitch   *condition.Static
	Recoder           *kernel.Recoder[*codec.NaiveDecoderFactory, *codec.NaiveEncoderFactory]
	MapStreamIndices  *kernel.MapStreamIndices
	Output            *kernel.Output

	RecodingConfig types.RecoderConfig

	nodeInput   *avpipeline.Node[*processor.FromKernel[*kernel.Input]]
	nodeRecoder *avpipeline.Node[*processor.FromKernel[*kernel.Recoder[*codec.NaiveDecoderFactory, *codec.NaiveEncoderFactory]]]
	nodeOutput  *avpipeline.Node[*processor.FromKernel[*kernel.Output]]

	locker    sync.Mutex
	waitGroup sync.WaitGroup
}

/*
//           +--> THROTTLE ->---+
// INPUT ->--+                  +--> (MAP INDICES) --> OUTPUT
//           +--> RECODER -->---+
*/
func New(ctx context.Context) *FFStream {
	s := &FFStream{
		FilterThrottle:    condition.NewVideoAverageBitrateLower(ctx, 0, 0),
		PassthroughSwitch: condition.NewSwitch(),
		BothPipesSwitch:   ptr(condition.Static(false)),
	}
	if switchOnlyOnKeyFrames {
		s.PassthroughSwitch.SetKeepUnless(condition.And{
			condition.MediaType(astiav.MediaTypeVideo),
			condition.IsKeyFrame(true),
		})
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
	switchValue := s.PassthroughSwitch.GetValue(ctx)
	logger.Tracef(ctx, "switchValue: %v", switchValue)
	if switchValue == 0 {
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
			var q quality.Quality = quality.ConstantBitrate(cfg.Video.AverageBitRate)
			if cfg.Video.AverageBitRate <= 0 {
				q = nil
			}
			err := encoder.SetQuality(ctx, q, nil)
			if err != nil {
				return fmt.Errorf("unable to set bitrate to %v: %w", cfg.Video.AverageBitRate, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	err = s.PassthroughSwitch.SetValue(ctx, 0)
	if err != nil {
		return fmt.Errorf("unable to switch to recoding: %w", err)
	}

	return nil
}

func (s *FFStream) reconfigureRecoderCopy(
	ctx context.Context,
	cfg types.RecoderConfig,
) error {
	err := s.PassthroughSwitch.SetValue(ctx, 1)
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

func (s *FFStream) GetAllStats(
	ctx context.Context,
) map[string]*avpipeline.ProcessingStatistics {
	return map[string]*avpipeline.ProcessingStatistics{
		"Input":   s.nodeInput.GetStats(),
		"Recoder": s.nodeRecoder.GetStats(),
		"Output":  s.nodeOutput.GetStats(),
	}
}

func tryNewBSF(
	ctx context.Context,
	codecID astiav.CodecID,
) *avpipeline.Node[*processor.FromKernel[*kernel.BitstreamFilter]] {
	recoderBSFName := bitstreamfilter.NameMP4ToAnnexB(codecID)
	if recoderBSFName == bitstreamfilter.NameNull {
		return nil
	}

	bitstreamFilter, err := kernel.NewBitstreamFilter(ctx, map[condition.Condition]bitstreamfilter.Name{
		condition.MediaType(astiav.MediaTypeVideo): recoderBSFName,
	})
	if err != nil {
		logger.Errorf(ctx, "unable to initialize the bitstream filter '%s': %w", recoderBSFName, err)
		return nil
	}

	return avpipeline.NewNodeFromKernel(
		ctx,
		bitstreamFilter,
		processor.DefaultOptionsOutput()...,
	)
}

func getVideoCodecNameFromStreams(streams []*astiav.Stream) astiav.CodecID {
	for _, stream := range streams {
		if stream.CodecParameters().MediaType() == astiav.MediaTypeVideo {
			return stream.CodecParameters().CodecID()
		}
	}
	return astiav.CodecIDNone
}

func (s *FFStream) Start(
	ctx context.Context,
	recoderInSeparateTracks bool,
) error {
	ctx, cancelFn := context.WithCancel(ctx)
	if s.Recoder == nil {
		return fmt.Errorf("Recoder is not configured")
	}

	// == configure ==

	s.nodeInput = avpipeline.NewNodeFromKernel(
		ctx,
		s.Input,
		processor.DefaultOptionsInput()...,
	)
	s.nodeRecoder = avpipeline.NewNodeFromKernel(
		ctx,
		s.Recoder,
		processor.DefaultOptionsRecoder()...,
	)
	nodeFilterThrottle := avpipeline.NewNodeFromKernel(
		ctx,
		kernel.NewPacketFilter(s.FilterThrottle, nil),
		processor.DefaultOptionsOutput()...,
	)
	s.nodeOutput = avpipeline.NewNodeFromKernel(
		ctx,
		s.Output,
		processor.DefaultOptionsOutput()...,
	)

	outputFormatName := s.nodeOutput.Processor.Kernel.FormatContext.OutputFormat().Name()
	logger.Infof(ctx, "output format: '%s'", outputFormatName)

	var nodeBSFRecoder, nodeBSFPassthrough *avpipeline.Node[*processor.FromKernel[*kernel.BitstreamFilter]]
	switch outputFormatName {
	case "mpegts", "rtsp":
		inputVideoCodecID := getVideoCodecNameFromStreams(
			s.nodeInput.Processor.Kernel.FormatContext.Streams(),
		)
		recodedVideoCodecID := s.Recoder.EncoderFactory.VideoCodecID()
		if recodedVideoCodecID == 0 { // vcodec: 'copy'
			recodedVideoCodecID = inputVideoCodecID
		}
		nodeBSFRecoder = tryNewBSF(ctx, recodedVideoCodecID)
		nodeBSFPassthrough = tryNewBSF(ctx, inputVideoCodecID)
	}

	audioFrameCount := 0
	keyFrameCount := 0
	bothPipesSwitch := condition.And{
		condition.Static(recoderInSeparateTracks),
		s.BothPipesSwitch,
		condition.Or{
			condition.And{
				condition.IsKeyFrame(true),
				condition.MediaType(astiav.MediaTypeVideo),
				condition.Function(func(ctx context.Context, pkt packet.Input) bool {
					keyFrameCount++
					if keyFrameCount%10 == 1 || true {
						logger.Debugf(ctx, "frame size: %d", len(pkt.Data()))
						return true
					}
					return false
				}),
			},
			condition.And{
				condition.MediaType(astiav.MediaTypeAudio),
				condition.Function(func(ctx context.Context, pkt packet.Input) bool {
					audioFrameCount++
					if audioFrameCount%10 == 1 || true {
						return true
					}
					return false
				}),
			},
			condition.Not{
				condition.MediaType(astiav.MediaTypeAudio),
				condition.MediaType(astiav.MediaTypeVideo),
			},
		},
	}

	var recoderOutput avpipeline.AbstractNode = s.nodeRecoder
	if autoInsertBitstreamFilters && nodeBSFRecoder != nil {
		logger.Debugf(ctx, "inserting %s to the recoder's output", nodeBSFRecoder.Processor.Kernel)
		recoderOutput.AddPushPacketsTo(nodeBSFRecoder)
		recoderOutput = nodeBSFRecoder
	}

	if passthroughSupport {
		var passthroughOutput avpipeline.AbstractNode = nodeFilterThrottle
		if autoInsertBitstreamFilters && nodeBSFPassthrough != nil {
			logger.Debugf(ctx, "inserting %s to the passthrough output", nodeBSFPassthrough.Processor.Kernel)
			passthroughOutput.AddPushPacketsTo(nodeBSFPassthrough)
			passthroughOutput = nodeBSFPassthrough
		}

		s.nodeInput.PushPacketsTo.Add(
			s.nodeRecoder,
			condition.Or{
				s.PassthroughSwitch.Condition(0),
				bothPipesSwitch,
			},
		)
		s.nodeInput.PushPacketsTo.Add(
			nodeFilterThrottle,
			condition.Or{
				s.PassthroughSwitch.Condition(1),
				bothPipesSwitch,
			},
		)

		if startWithPassthrough {
			s.PassthroughSwitch.SetValue(ctx, 1)
		}

		if recoderInSeparateTracks {
			*s.BothPipesSwitch = true
			nodeMapStreamIndices := avpipeline.NewNodeFromKernel(
				ctx,
				s.MapStreamIndices,
				processor.DefaultOptionsOutput()...,
			)
			recoderOutput.AddPushPacketsTo(
				nodeMapStreamIndices,
			)
			passthroughOutput.AddPushPacketsTo(
				nodeMapStreamIndices,
			)
			nodeMapStreamIndices.AddPushPacketsTo(s.nodeOutput)
		} else {
			if !startWithPassthrough || notifyAboutPacketSources {
				nodeFilterThrottle.InputPacketCondition = filter.NewRescaleTSBetweenKernels(
					s.nodeInput.Processor.Kernel,
					s.nodeRecoder.Processor.Kernel,
				)
			} else {
				logger.Warnf(ctx, "unable to configure rescale_ts because startWithPassthrough && !notifyAboutPacketSources")
			}

			recoderOutput.AddPushPacketsTo(
				s.nodeOutput,
				s.PassthroughSwitch.Condition(0),
			)
			passthroughOutput.AddPushPacketsTo(
				s.nodeOutput,
				s.PassthroughSwitch.Condition(1),
			)
		}
	} else {
		s.nodeInput.AddPushPacketsTo(s.nodeRecoder)
		recoderOutput.AddPushPacketsTo(s.nodeOutput)
	}

	// == spawn an observer ==

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

	// == prepare ==

	if notifyAboutPacketSources {
		err := avpipeline.NotifyAboutPacketSourcesRecursively(ctx, nil, s.nodeInput)
		if err != nil {
			return fmt.Errorf("receive an error while notifying nodes about packet sources: %w", err)
		}
	}
	logger.Infof(ctx, "resulting pipeline: %s", s.nodeInput.String())
	logger.Infof(ctx, "resulting pipeline: %s", s.nodeInput.DotString(false))

	// == launch ==

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
