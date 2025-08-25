package ffstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline"
	"github.com/xaionaro-go/avpipeline/codec"
	codectypes "github.com/xaionaro-go/avpipeline/codec/types"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/node"
	streammux "github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/avpipeline/processor"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/goconv"
	"github.com/xaionaro-go/observability"
	"github.com/xaionaro-go/xsync"
)

type FFStream struct {
	NodeInput       *node.Node[*processor.FromKernel[*kernel.Input]]
	OutputTemplates []OutputTemplate

	StreamMux *streammux.StreamMux[struct{}]

	TolerableOutputQueueSizeBytes atomic.Uint64
	CurrentOutputBufferSize       xsync.Map[int, uint64]

	cancelFunc context.CancelFunc
	locker     sync.Mutex
}

func New(ctx context.Context) *FFStream {
	s := &FFStream{}
	return s
}

func (s *FFStream) addCancelFnLocked(cancelFn context.CancelFunc) {
	if s.cancelFunc == nil {
		s.cancelFunc = cancelFn
		return
	}

	oldCancelFn := s.cancelFunc
	s.cancelFunc = func() {
		cancelFn()
		oldCancelFn()
	}
}

func (s *FFStream) AddInput(
	ctx context.Context,
	input *kernel.Input,
) error {
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.NodeInput != nil {
		return fmt.Errorf("currently we support only one input")
	}
	ctx, cancelFn := context.WithCancel(ctx)
	s.addCancelFnLocked(cancelFn)
	s.NodeInput = node.NewFromKernel(ctx, input, processor.DefaultOptionsInput()...)
	return nil
}

func (s *FFStream) AddOutputTemplate(
	ctx context.Context,
	outputTemplate OutputTemplate,
) (_err error) {
	logger.Debugf(ctx, "AddOutputTemplate(ctx, %#+v)", outputTemplate)
	defer func() { logger.Debugf(ctx, "/AddOutputTemplate(ctx, %#+v): %v", outputTemplate, _err) }()
	s.locker.Lock()
	defer s.locker.Unlock()
	s.OutputTemplates = append(s.OutputTemplates, outputTemplate)
	return nil
}

func (s *FFStream) GetRecoderConfig(
	ctx context.Context,
) (_ret streammuxtypes.RecoderConfig) {
	return s.StreamMux.GetRecoderConfig(ctx)
}

func (s *FFStream) SetRecoderConfig(
	ctx context.Context,
	cfg streammuxtypes.RecoderConfig,
) (_err error) {
	logger.Debugf(ctx, "SetRecoderConfig(ctx, %#+v)", cfg)
	defer func() {
		logger.Debugf(ctx, "/SetRecoderConfig(ctx, %#+v): %v", cfg, _err)
	}()
	if s.StreamMux == nil {
		return fmt.Errorf("it is allowed to use SetRecoderConfig only after Start is invoked")
	}
	if len(cfg.VideoTrackConfigs) > 0 {
		videoCfg := &cfg.VideoTrackConfigs[0]
		if videoCfg.CodecName != codectypes.Name(codec.NameCopy) && videoCfg.Resolution == (codec.Resolution{}) {
			return fmt.Errorf("resolution must be set for video codec %q", videoCfg.CodecName)
		}
	}
	return s.StreamMux.SetRecoderConfig(ctx, cfg)
}

func (s *FFStream) GetStats(
	ctx context.Context,
) *ffstream_grpc.GetStatsReply {
	if s == nil {
		return nil
	}
	r := &ffstream_grpc.GetStatsReply{
		Packets: &ffstream_grpc.CommonsProcessingPacketsOrFramesStatistics{
			Wrote: &ffstream_grpc.CommonsProcessingPacketsOrFramesStatisticsSection{},
		},
		Frames: &ffstream_grpc.CommonsProcessingPacketsOrFramesStatistics{
			Wrote: &ffstream_grpc.CommonsProcessingPacketsOrFramesStatisticsSection{},
		},
	}
	if s.NodeInput != nil {
		r.BytesCountRead = s.NodeInput.Statistics.BytesCountWrote.Load()
		r.Packets.Read = goconv.ProcessingPacketsOrFramesStatisticsSectionToGRPC(&s.NodeInput.Statistics.Packets.Wrote)
		r.Frames.Read = goconv.ProcessingPacketsOrFramesStatisticsSectionToGRPC(&s.NodeInput.Statistics.Frames.Wrote)
	}
	if s.StreamMux != nil {
		for _, output := range s.StreamMux.Outputs {
			stats := output.OutputNode.GetStatistics().Convert()
			r.BytesCountWrote += stats.BytesCountRead
			r.Packets.Wrote.Unknown += stats.Packets.Read.Unknown
			r.Packets.Wrote.Other += stats.Packets.Read.Other
			r.Packets.Wrote.Video += stats.Packets.Read.Video
			r.Packets.Wrote.Audio += stats.Packets.Read.Audio
		}
	}
	return r
}

func (s *FFStream) GetAllStats(
	ctx context.Context,
) map[string]*node.ProcessingStatistics {
	return s.StreamMux.GetAllStats(ctx)
}

func (s *FFStream) SetTolerableOutputQueueSizeBytes(
	ctx context.Context,
	newValue uint64,
) {
	s.TolerableOutputQueueSizeBytes.Store(newValue)
}

func (s *FFStream) GetTolerableOutputQueueSizeBytes(
	ctx context.Context,
) uint64 {
	return s.TolerableOutputQueueSizeBytes.Load()
}

func (s *FFStream) Start(
	ctx context.Context,
	recoderConfig streammuxtypes.RecoderConfig,
	muxMode streammuxtypes.MuxMode,
	autoBitRate *streammuxtypes.AutoBitRateConfig,
) (_err error) {
	logger.Debugf(ctx, "Start")
	defer func() { logger.Debugf(ctx, "/Start: %v", _err) }()

	if s.StreamMux != nil {
		return fmt.Errorf("this ffstream was already used")
	}
	if s.NodeInput == nil {
		return fmt.Errorf("no inputs added")
	}
	if len(s.OutputTemplates) != 1 {
		return fmt.Errorf("exactly one output template is required, got %d", len(s.OutputTemplates))
	}

	ctx, cancelFn := context.WithCancel(ctx)
	defer func() {
		if _err != nil {
			cancelFn()
		}
	}()
	s.addCancelFnLocked(cancelFn)

	var err error
	s.StreamMux, err = streammux.New(
		ctx,
		muxMode,
		autoBitRate,
		s.asOutputFactory(),
	)
	if err != nil {
		return fmt.Errorf("unable to initialize a streammux: %w", err)
	}
	s.NodeInput.AddPushPacketsTo(s.StreamMux.Input())

	if err := s.SetRecoderConfig(ctx, recoderConfig); err != nil {
		return fmt.Errorf("SetRecoderConfig(%#+v): %w", recoderConfig, err)
	}

	if autoBitRate != nil {
		outputKey := streammux.OutputKeyFromRecoderConfig(ctx, &recoderConfig)
		var wg sync.WaitGroup
		for _, output := range s.StreamMux.AutoBitRateHandler.ResolutionsAndBitRates {
			outputKey.Resolution = output.Resolution
			wg.Add(1)
			go func(output streammuxtypes.OutputKey) {
				defer wg.Done()
				if _, err := s.StreamMux.InitOutput(ctx, outputKey); err != nil {
					logger.Errorf(ctx, "unable to init output for resolution %#+v: %w", output.Resolution, err)
				}
			}(outputKey)
		}
		wg.Wait()
	}

	errCh := make(chan node.Error, 100)
	observability.Go(ctx, func(ctx context.Context) {
		defer close(errCh)
		avpipeline.Serve(ctx, avpipeline.ServeConfig{}, errCh, s.NodeInput)
	})

	observability.Go(ctx, func(ctx context.Context) {
		defer s.cancelFunc()
		select {
		case <-ctx.Done():
		case err, ok := <-errCh:
			if !ok {
				logger.Debugf(ctx, "the error channel is closed")
				return
			}

			if errors.Is(err.Err, context.Canceled) {
				logger.Debugf(ctx, "cancelled: %#+v", err)
				return
			}
			if errors.Is(err.Err, io.EOF) {
				logger.Debugf(ctx, "EOF: %#+v", err)
				return
			}
			logger.Errorf(ctx, "stopping because received error: %v", err)
			return
		}
	})

	err = s.StreamMux.WaitForStart(ctx)
	if err != nil {
		return fmt.Errorf("unable to wait for streammux's start: %w", err)
	}

	return nil
}

func (s *FFStream) Wait(
	ctx context.Context,
) (_err error) {
	logger.Debugf(ctx, "Wait")
	defer func() { logger.Debugf(ctx, "/Wait: %v", _err) }()
	return s.StreamMux.WaitForStop(ctx)
}
func (s *FFStream) GetAutoBitRateCalculator(
	ctx context.Context,
) streammux.AutoBitRateCalculator {
	if s == nil {
		return nil
	}
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.StreamMux == nil || s.StreamMux.AutoBitRateHandler == nil {
		return nil
	}
	return s.StreamMux.AutoBitRateHandler.Calculator
}

func (s *FFStream) SetAutoBitRateCalculator(
	ctx context.Context,
	calculator streammux.AutoBitRateCalculator,
) (_err error) {
	logger.Debugf(ctx, "SetAutoBitRateCalculator(ctx, %#+v)", calculator)
	defer func() { logger.Debugf(ctx, "/SetAutoBitRateCalculator(ctx, %#+v): %v", calculator, _err) }()

	s.locker.Lock()
	defer s.locker.Unlock()
	if s.StreamMux == nil || s.StreamMux.AutoBitRateHandler == nil {
		return fmt.Errorf("it is allowed to use SetAutoBitRateCalculator only after Start is invoked with non-nil AutoBitRateConfig")
	}
	s.StreamMux.AutoBitRateHandler.Calculator = calculator
	return nil
}

func (s *FFStream) GetFPSFraction(
	ctx context.Context,
) (num uint32, den uint32, err error) {
	if s == nil {
		return 0, 1, fmt.Errorf("ffstream is nil")
	}
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.StreamMux == nil {
		return 0, 1, fmt.Errorf("it is allowed to use GetFPSFraction only after Start is invoked")
	}
	num, den = s.StreamMux.GetFPSFraction(ctx)
	return num, den, nil
}

func (s *FFStream) SetFPSFraction(
	ctx context.Context,
	num uint32,
	den uint32,
) (_err error) {
	logger.Debugf(ctx, "SetFPSFraction(ctx, %d/%d)", num, den)
	defer func() { logger.Debugf(ctx, "/SetFPSFraction(ctx, %d/%d): %v", num, den, _err) }()

	s.locker.Lock()
	defer s.locker.Unlock()
	if s.StreamMux == nil {
		return fmt.Errorf("it is allowed to use SetFPSFraction only after Start is invoked")
	}
	if den == 0 {
		return fmt.Errorf("den must be non-zero")
	}
	if num%den != 0 {
		return fmt.Errorf("divider must be an integer fraction (num divisible by den), got %d/%d", num, den)
	}
	s.StreamMux.SetFPSFraction(ctx, num, den)
	return nil
}
