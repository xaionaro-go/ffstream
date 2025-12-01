package ffstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline"
	"github.com/xaionaro-go/avpipeline/codec"
	codectypes "github.com/xaionaro-go/avpipeline/codec/types"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/node"
	packetfiltercondition "github.com/xaionaro-go/avpipeline/node/filter/packetfilter/condition"
	"github.com/xaionaro-go/avpipeline/packet/condition/extra"
	"github.com/xaionaro-go/avpipeline/packet/condition/extra/quality"
	streammux "github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/avpipeline/processor"
	avpipeline_grpc "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
	goconvavp "github.com/xaionaro-go/avpipeline/protobuf/goconv/avpipeline"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/goconv"
	"github.com/xaionaro-go/observability"
)

type FFStream struct {
	NodeInput       *node.Node[*processor.FromKernel[*kernel.Input]]
	OutputTemplates []SenderTemplate

	StreamMux *streammux.StreamMux[CustomData]

	InputQualityMeasurer  *quality.Measurements
	OutputQualityMeasurer *extra.QualityT

	cancelFunc context.CancelFunc
	locker     sync.Mutex
}

func New(ctx context.Context) *FFStream {
	s := &FFStream{
		InputQualityMeasurer:  quality.NewMeasurements(),
		OutputQualityMeasurer: extra.NewQuality(),
	}
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
	outputTemplate SenderTemplate,
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

func (s *FFStream) SwitchOutputByProps(
	ctx context.Context,
	props streammuxtypes.SenderProps,
) (_err error) {
	logger.Debugf(ctx, "ModifyOutput(ctx, %#+v)", props)
	defer func() {
		logger.Debugf(ctx, "/ModifyOutput(ctx, %#+v): %v", props, _err)
	}()
	if s.StreamMux == nil {
		return fmt.Errorf("it is allowed to use ModifyOutput only after Start is invoked")
	}
	if len(props.Output.AudioTrackConfigs) > 0 {
		audioCfg := &props.Output.AudioTrackConfigs[0]
		if audioCfg.CodecName != codectypes.Name(codec.NameCopy) && audioCfg.SampleRate == 0 {
			return fmt.Errorf("sample rate must be set for audio codec %q", audioCfg.CodecName)
		}
	}
	if len(props.Output.VideoTrackConfigs) > 0 {
		videoCfg := &props.Output.VideoTrackConfigs[0]
		if videoCfg.CodecName != codectypes.Name(codec.NameCopy) && videoCfg.Resolution == (codec.Resolution{}) {
			return fmt.Errorf("resolution must be set for video codec %q", videoCfg.CodecName)
		}
	}
	return s.StreamMux.SwitchToOutputByProps(ctx, props)
}

func (s *FFStream) GetStats(
	ctx context.Context,
) *ffstream_grpc.GetStatsReply {
	if s == nil {
		return nil
	}
	r := &ffstream_grpc.GetStatsReply{
		NodeCounters: &avpipeline_grpc.NodeCounters{
			Received:  &avpipeline_grpc.NodeCountersSection{},
			Processed: &avpipeline_grpc.NodeCountersSection{},
			Missed:    &avpipeline_grpc.NodeCountersSection{},
			Generated: &avpipeline_grpc.NodeCountersSection{},
			Sent:      &avpipeline_grpc.NodeCountersSection{},
		},
	}
	if s.NodeInput != nil {
		inputCounters := goconvavp.NodeCountersToGRPC(s.NodeInput.GetCountersPtr(), s.NodeInput.GetProcessor().CountersPtr())
		r.NodeCounters.Received = inputCounters.Received
	}
	if s.StreamMux != nil {
		s.StreamMux.Outputs.Range(func(_ streammux.OutputID, output *streammux.Output[CustomData]) bool {
			outputCounters := goconvavp.NodeCountersToGRPC(
				output.SendingNode.GetCountersPtr(),
				output.SendingNode.GetProcessor().CountersPtr(),
			)
			r.NodeCounters.Processed = goconv.AddNodeCountersSection(r.NodeCounters.Processed, outputCounters.Processed)
			r.NodeCounters.Missed = goconv.AddNodeCountersSection(r.NodeCounters.Missed, outputCounters.Missed)
			r.NodeCounters.Generated = goconv.AddNodeCountersSection(r.NodeCounters.Generated, outputCounters.Generated)
			r.NodeCounters.Sent = goconv.AddNodeCountersSection(r.NodeCounters.Sent, outputCounters.Sent)
			return true
		})
	}
	return r
}

func (s *FFStream) GetAllStats(
	ctx context.Context,
) map[string]avptypes.Statistics {
	return s.StreamMux.GetAllStats(ctx)
}

func (s *FFStream) Start(
	ctx context.Context,
	recoderConfig streammuxtypes.RecoderConfig,
	muxMode streammuxtypes.MuxMode,
	autoBitRateVideo *streammuxtypes.AutoBitRateVideoConfig,
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
	s.StreamMux, err = streammux.NewWithCustomData(
		ctx,
		muxMode,
		s.asSenderFactory(),
	)
	if err != nil {
		return fmt.Errorf("unable to initialize a streammux: %w", err)
	}

	if err := s.StreamMux.SetAutoBitRateVideoConfig(ctx, autoBitRateVideo); err != nil {
		return fmt.Errorf("unable to set the auto-bitrate config %#+v: %w", autoBitRateVideo, err)
	}

	s.NodeInput.AddPushPacketsTo(ctx, s.StreamMux, packetfiltercondition.Function(s.onInputPacket))

	if err := s.SwitchOutputByProps(ctx, streammuxtypes.SenderProps{
		RecoderConfig:   recoderConfig,
		SenderNodeProps: streammuxtypes.SenderNodeProps{},
	}); err != nil {
		return fmt.Errorf("SetRecoderConfig(%#+v): %w", recoderConfig, err)
	}

	if autoBitRateVideo != nil {
		senderKey := streammux.PartialSenderKeyFromRecoderConfig(ctx, &recoderConfig)
		var wg sync.WaitGroup
		for _, output := range s.StreamMux.AutoBitRateHandler.ResolutionsAndBitRates {
			senderKey.VideoResolution = output.Resolution
			wg.Add(1)
			go func(output streammuxtypes.SenderKey) {
				defer wg.Done()
				switch s.StreamMux.MuxMode {
				case streammuxtypes.MuxModeDifferentOutputsSameTracks:
					if _, _, err := s.StreamMux.GetOrCreateOutput(ctx, senderKey); err != nil {
						logger.Errorf(ctx, "unable to create output for resolution %#+v: %v", output.VideoResolution, err)
					}
				case streammuxtypes.MuxModeDifferentOutputsSameTracksSplitAV:
					if _, _, err := s.StreamMux.GetOrCreateOutput(ctx, streammuxtypes.SenderKey{
						VideoCodec:      senderKey.VideoCodec,
						VideoResolution: senderKey.VideoResolution,
					}); err != nil {
						logger.Errorf(ctx, "unable to create output for resolution %#+v: %v", output.VideoResolution, err)
					}
				}
			}(senderKey)
		}
		if autoBitRateVideo.AutoByPass {
			wg.Add(1)
			go func() {
				defer wg.Done()
				switch s.StreamMux.MuxMode {
				case streammuxtypes.MuxModeDifferentOutputsSameTracks:
					if _, _, err := s.StreamMux.GetOrCreateOutput(ctx, streammuxtypes.SenderKey{
						AudioCodec:      senderKey.AudioCodec,
						AudioSampleRate: senderKey.AudioSampleRate,
						VideoCodec:      codectypes.NameCopy,
					}); err != nil {
						logger.Errorf(ctx, "unable to init output for the bypass: %v", err)
					}
				case streammuxtypes.MuxModeDifferentOutputsSameTracksSplitAV:
					if _, _, err := s.StreamMux.GetOrCreateOutput(ctx, streammuxtypes.SenderKey{
						VideoCodec: codectypes.NameCopy,
					}); err != nil {
						logger.Errorf(ctx, "unable to init output for the bypass: %v", err)
					}
				}
			}()
		}
		wg.Wait()
	}

	errCh := make(chan node.Error, 100)
	observability.Go(ctx, func(ctx context.Context) {
		defer close(errCh)
		avpipeline.Serve(ctx, avpipeline.ServeConfig{
			EachNode: node.ServeConfig{},
		}, errCh, s.NodeInput)
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

func (s *FFStream) GetAutoBitRateVideoConfig(
	ctx context.Context,
) (_ret *streammuxtypes.AutoBitRateVideoConfig, err error) {
	if s == nil {
		return nil, fmt.Errorf("ffstream is nil")
	}
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.StreamMux == nil {
		return nil, fmt.Errorf("it is allowed to use GetAutoBitRateVideoConfig only after Start is invoked")
	}

	h := s.StreamMux.GetAutoBitRateHandler()
	if h == nil {
		return nil, nil
	}

	return &h.AutoBitRateVideoConfig, nil
}

func (s *FFStream) SetAutoBitRateVideoConfig(
	ctx context.Context,
	cfg *streammuxtypes.AutoBitRateVideoConfig,
) (_err error) {
	logger.Debugf(ctx, "SetAutoBitRateVideoConfig(ctx, %#+v)", cfg)
	defer func() { logger.Debugf(ctx, "/SetAutoBitRateVideoConfig(ctx, %#+v): %v", cfg, _err) }()

	s.locker.Lock()
	defer s.locker.Unlock()
	if s.StreamMux == nil {
		return fmt.Errorf("it is allowed to use SetAutoBitRateVideoConfig only after Start is invoked")
	}

	if err := s.StreamMux.SetAutoBitRateVideoConfig(ctx, cfg); err != nil {
		return fmt.Errorf("unable to set the auto-bitrate config %#+v: %w", cfg, err)
	}
	return nil
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
	fps := s.StreamMux.GetFPSFraction(ctx)
	num = uint32(fps.Num)
	den = uint32(fps.Den)
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

func (s *FFStream) GetBitRates(
	ctx context.Context,
) (_ret *streammuxtypes.BitRates, err error) {
	if s == nil {
		return nil, fmt.Errorf("ffstream is nil")
	}
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.StreamMux == nil {
		return nil, fmt.Errorf("it is allowed to use GetBitRates only after Start is invoked")
	}

	bitRates, err := s.StreamMux.GetBitRates(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get bit rates: %w", err)
	}
	return bitRates, nil
}

func (s *FFStream) GetLatencies(
	ctx context.Context,
) (_ret *streammuxtypes.Latencies, err error) {
	if s == nil {
		return nil, fmt.Errorf("ffstream is nil")
	}
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.StreamMux == nil {
		return nil, fmt.Errorf("it is allowed to use GetLatencies only after Start is invoked")
	}

	latencies, err := s.StreamMux.GetLatencies(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get latencies: %w", err)
	}
	return latencies, nil
}

func (s *FFStream) onInputPacket(
	ctx context.Context,
	packet packetfiltercondition.Input,
) bool {
	s.InputQualityMeasurer.ObservePacket(ctx, packet.Input)
	return true
}

func (s *FFStream) GetInputQuality(
	ctx context.Context,
) (_ret *quality.QualityAggregated, err error) {
	r, err := s.InputQualityMeasurer.GetQuality(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting input quality: %w", err)
	}
	return r.Aggregate(), nil
}

func (s *FFStream) GetOutputQuality(
	ctx context.Context,
) (_ret *quality.QualityAggregated, err error) {
	r, err := s.OutputQualityMeasurer.Measurements.GetQuality(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting output quality: %w", err)
	}
	return r.Aggregate(), nil
}
