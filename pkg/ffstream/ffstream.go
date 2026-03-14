// Package ffstream provides a high-level media streaming controller built on avpipeline.
// It manages multiple prioritized inputs with automatic fallback, stream multiplexing,
// and dynamic output generation with support for quality monitoring and SRT.
//
// ffstream.go defines the FFStream struct which coordinates inputs, multiplexing, and quality monitoring.
package ffstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/asticode/go-astiav"
	"github.com/davecgh/go-spew/spew"
	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline"
	"github.com/xaionaro-go/avpipeline/codec"
	codectypes "github.com/xaionaro-go/avpipeline/codec/types"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/node"
	packetorframefiltercondition "github.com/xaionaro-go/avpipeline/node/filter/packetorframefilter/condition"
	"github.com/xaionaro-go/avpipeline/packet"
	"github.com/xaionaro-go/avpipeline/packet/condition/extra"
	"github.com/xaionaro-go/avpipeline/packetorframe"
	"github.com/xaionaro-go/avpipeline/packetorframe/filter/quality"
	"github.com/xaionaro-go/avpipeline/preset/inputwithfallback"
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

const (
	enableGapFiller = false
)

type (
	Inputs     = inputwithfallback.InputWithFallback[*Input, *DecoderFactory, CustomData]
	InputChain = inputwithfallback.InputChain[*Input, *DecoderFactory, CustomData]
)

type FFStream struct {
	Config          Config
	Inputs          *Inputs
	InputsInfo      []Resources
	OutputTemplates []SenderTemplate

	StreamMux *streammux.StreamMux[CustomData]

	InputQualityMeasurer  *quality.Measurements
	OutputQualityMeasurer *extra.QualityT

	audioSync *kernel.AudioSync

	cancelFunc context.CancelFunc
	locker     sync.Mutex

	audioStreamIndices map[fallbackResourceKey]int
}

type fallbackResourceKey struct {
	Priority    uint
	ResourceIdx ResourceIndex
}

func New(
	ctx context.Context,
	opts ...Option,
) (*FFStream, error) {
	cfg := Options(opts).Config()

	var inputOpts []inputwithfallback.Option
	inputOpts = append(inputOpts, inputwithfallback.OptionRetryInterval(cfg.InputRetryInterval))
	inputs, err := inputwithfallback.New[*Input, *DecoderFactory, CustomData](ctx, nil, inputOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to create the inputs handler: %w", err)
	}
	s := &FFStream{
		Config:                cfg,
		Inputs:                inputs,
		InputQualityMeasurer:  quality.NewMeasurements(),
		OutputQualityMeasurer: extra.NewQuality(),
		audioStreamIndices:    make(map[fallbackResourceKey]int),
	}
	return s, nil
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
	resource Resource,
) (_err error) {
	logger.Debugf(ctx, "AddInput(ctx, %#+v)", resource)
	defer func() { logger.Debugf(ctx, "/AddInput(ctx, %#+v): %v", resource, _err) }()
	s.locker.Lock()
	defer s.locker.Unlock()

	priority := resource.GetFallbackPriority(ctx)

	// If the requested priority level doesn't exist yet, create new input factories
	// for all missing priority levels up to the requested one.
	startLen := len(s.InputsInfo)
	for p := len(s.Inputs.InputChains); p <= int(priority); p++ {
		s.InputsInfo = append(s.InputsInfo, nil)
		if err := s.Inputs.AddFactory(ctx, newInputFactory(s, uint(p))); err != nil {
			s.InputsInfo = s.InputsInfo[:startLen]
			return fmt.Errorf("failed to add input factory: %w", err)
		}
	}

	s.InputsInfo[priority] = append(s.InputsInfo[priority], resource)
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

func (s *FFStream) GetTranscoderConfig(
	ctx context.Context,
) (_ret streammuxtypes.TranscoderConfig) {
	return s.StreamMux.GetTranscoderConfig(ctx)
}

func (s *FFStream) SwitchOutputByProps(
	ctx context.Context,
	props streammuxtypes.SenderProps,
) (_err error) {
	logger.Debugf(ctx, "SwitchOutputByProps(ctx, %#+v)", props)
	defer func() {
		logger.Debugf(ctx, "/SwitchOutputByProps(ctx, %#+v): %v", props, _err)
	}()
	if s.StreamMux == nil {
		return fmt.Errorf("it is allowed to use SwitchOutputByProps only after Start is invoked")
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
	if s.Inputs != nil {
		inputCounters := goconvavp.NodeCountersToGRPC(s.Inputs.GetCountersPtr(), s.Inputs.GetProcessor().CountersPtr())
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
	transcoderConfig streammuxtypes.TranscoderConfig,
	muxMode streammuxtypes.MuxMode,
	autoBitRateVideo *streammuxtypes.AutoBitRateVideoConfig,
) (_err error) {
	logger.Debugf(ctx, "Start")
	defer func() { logger.Debugf(ctx, "/Start: %v", _err) }()

	if s.StreamMux != nil {
		return fmt.Errorf("this ffstream was already used")
	}
	if s.Inputs.GetInputChainsCount(ctx) == 0 {
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

	s.audioSync = kernel.NewAudioSync(ctx, nil)
	syncNode := node.NewFromKernel(ctx, s.audioSync)

	// Observe quality metrics and forward all inputs to AudioSync.
	s.Inputs.AddPushTo(ctx, syncNode, packetorframefiltercondition.Function(s.onInput))

	if enableGapFiller {
		gapCfg := kernel.DefaultGapFillerConfig()
		gapCfg.OverlapStrategyAudio = kernel.OverlapStrategyAudioSpeedUp
		gapFillerNode := node.NewFromKernel(ctx, kernel.NewGapFiller(ctx, &gapCfg))
		syncNode.AddPushTo(ctx, gapFillerNode, packetorframefiltercondition.Function(s.shouldForwardToOutput))
		gapFillerNode.AddPushTo(ctx, s.StreamMux)
	} else {
		syncNode.AddPushTo(ctx, s.StreamMux, packetorframefiltercondition.Function(s.shouldForwardToOutput))
	}

	if err := s.SwitchOutputByProps(ctx, streammuxtypes.SenderProps{
		TranscoderConfig: transcoderConfig,
		SenderNodeProps:  streammuxtypes.SenderNodeProps{},
	}); err != nil {
		return fmt.Errorf("SwitchOutputByProps(%#+v): %w", transcoderConfig, err)
	}

	if autoBitRateVideo != nil {
		s.preemptivelyInitAutoBitRateOutputs(ctx, transcoderConfig, autoBitRateVideo)
	}

	errCh := make(chan node.Error, 100)
	observability.Go(ctx, func(ctx context.Context) {
		defer close(errCh)
		avpipeline.Serve(ctx, avpipeline.ServeConfig{
			EachNode: node.ServeConfig{},
		}, errCh, []node.Abstract{s.Inputs}...)
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

func (s *FFStream) InjectSubtitles(
	ctx context.Context,
	data []byte,
	duration time.Duration,
) error {
	logger.Debugf(ctx, "InjectSubtitles(ctx, %d bytes, %v)", len(data), duration)
	if s.StreamMux == nil {
		return fmt.Errorf("ffstream is not started")
	}

	pkt := packet.Pool.Get()
	// Do not free pkt here, it will be freed by the pipeline

	if err := pkt.AllocPayload(len(data)); err != nil {
		packet.Pool.Put(pkt)
		return fmt.Errorf("unable to allocate payload for subtitle packet: %w", err)
	}
	copy(pkt.Data(), data)

	// Use the last seen audio DTS as the base for PTS/DTS
	dts := s.StreamMux.Measurements[astiav.MediaTypeAudio].InputDTS.Load()
	if dts == 0 {
		dts = s.StreamMux.Measurements[astiav.MediaTypeVideo].InputDTS.Load()
	}

	pkt.SetPts(int64(dts))
	pkt.SetDts(int64(dts))
	pkt.SetDuration(int64(duration / time.Millisecond))

	source := any(s.StreamMux.InputAll.Node.Processor).(processor.GetPacketSourcer).GetPacketSource()
	streamInfo := &packet.StreamInfo{
		CodecParameters: astiav.AllocCodecParameters(),
		TimeBase:        astiav.NewRational(1, 1000), // milliseconds
		StreamIndex:     2,
	}
	streamInfo.CodecParameters.SetCodecID(astiav.CodecIDText)
	streamInfo.CodecParameters.SetMediaType(astiav.MediaTypeSubtitle)

	inputPkt := packet.BuildInput(pkt, streamInfo)
	inputPkt.SetStreamIndex(2)
	inputPkt.Source = source

	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.StreamMux.InputChan() <- packetorframe.InputUnion{Packet: &inputPkt}:
	}

	return nil
}

func (s *FFStream) InjectData(
	ctx context.Context,
	data []byte,
	duration time.Duration,
) error {
	logger.Debugf(ctx, "InjectData(ctx, %d bytes, %v)", len(data), duration)
	if s.StreamMux == nil {
		return fmt.Errorf("ffstream is not started")
	}

	pkt := packet.Pool.Get()
	// Do not free pkt here, it will be freed by the pipeline

	if err := pkt.AllocPayload(len(data)); err != nil {
		packet.Pool.Put(pkt)
		return fmt.Errorf("unable to allocate payload for data packet: %w", err)
	}
	copy(pkt.Data(), data)

	// Use the last seen audio DTS as the base for PTS/DTS
	dts := s.StreamMux.Measurements[astiav.MediaTypeAudio].InputDTS.Load()
	if dts == 0 {
		dts = s.StreamMux.Measurements[astiav.MediaTypeVideo].InputDTS.Load()
	}

	pkt.SetPts(int64(dts))
	pkt.SetDts(int64(dts))
	pkt.SetDuration(int64(duration / time.Millisecond))

	source := any(s.StreamMux.InputAll.Node.Processor).(processor.GetPacketSourcer).GetPacketSource()
	streamInfo := &packet.StreamInfo{
		CodecParameters: astiav.AllocCodecParameters(),
		TimeBase:        astiav.NewRational(1, 1000), // milliseconds
		StreamIndex:     3,
	}
	streamInfo.CodecParameters.SetCodecID(astiav.CodecIDBinData)
	streamInfo.CodecParameters.SetMediaType(astiav.MediaTypeData)

	inputPkt := packet.BuildInput(pkt, streamInfo)
	inputPkt.SetStreamIndex(3)
	inputPkt.Source = source

	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.StreamMux.InputChan() <- packetorframe.InputUnion{Packet: &inputPkt}:
	}

	return nil
}

func (s *FFStream) preemptivelyInitAutoBitRateOutputs(
	ctx context.Context,
	transcoderConfig streammuxtypes.TranscoderConfig,
	autoBitRateVideo *streammuxtypes.AutoBitRateVideoConfig,
) {
	senderKey := streammux.PartialSenderKeyFromTranscoderConfig(ctx, &transcoderConfig)
	for _, output := range s.StreamMux.AutoBitRateHandler.ResolutionsAndBitRates {
		senderKey.VideoResolution = output.Resolution
		senderKey := senderKey
		observability.Go(ctx, func(ctx context.Context) {
			switch s.StreamMux.MuxMode {
			case streammuxtypes.MuxModeDifferentOutputsSameTracks:
				if _, _, err := s.StreamMux.GetOrCreateOutput(ctx, senderKey); err != nil {
					logger.Errorf(ctx, "unable to create output for resolution %#+v: %v", senderKey.VideoResolution, err)
				}
			case streammuxtypes.MuxModeDifferentOutputsSameTracksSplitAV:
				if _, _, err := s.StreamMux.GetOrCreateOutput(ctx, streammuxtypes.SenderKey{
					VideoCodec:      senderKey.VideoCodec,
					VideoResolution: senderKey.VideoResolution,
				}); err != nil {
					logger.Errorf(ctx, "unable to create output for resolution %#+v: %v", senderKey.VideoResolution, err)
				}
			}
		})
	}
	if autoBitRateVideo.AutoByPass {
		observability.Go(ctx, func(ctx context.Context) {
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
		})
	}
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
	s.StreamMux.SetFPSFraction(ctx, avptypes.Rational{
		Num: int(num),
		Den: int(den),
	})
	return nil
}

func (s *FFStream) SetSuppressed(
	ctx context.Context,
	priority uint,
	num ResourceIndex,
	suppressed bool,
) error {
	s.locker.Lock()
	defer s.locker.Unlock()

	if int(priority) >= len(s.InputsInfo) {
		return fmt.Errorf("input priority %d is out of range", priority)
	}
	if int(num) >= len(s.InputsInfo[priority]) {
		return fmt.Errorf("input num %d is out of range at priority %d", num, priority)
	}

	s.InputsInfo[priority][num].Suppressed = suppressed
	return nil
}

func (s *FFStream) GetBitRates(
	ctx context.Context,
) (_ret *streammuxtypes.BitRates, err error) {
	logger.Debugf(ctx, "GetBitRates")
	defer func() { logger.Debugf(ctx, "/GetBitRates: %#+v, %v", _ret, err) }()
	if s == nil {
		return nil, fmt.Errorf("ffstream is nil")
	}
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.StreamMux == nil {
		return nil, fmt.Errorf("it is allowed to use GetBitRates only after Start is invoked")
	}

	bitRatesIn := s.Inputs.GetBitRates(ctx)

	bitRatesOut, err := s.StreamMux.GetBitRates(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get bit rates: %w", err)
	}

	logger.Debugf(ctx, "input=%s, output=%s", spew.Sdump(bitRatesIn), spew.Sdump(bitRatesOut))

	return &streammuxtypes.BitRates{
		Input:   bitRatesIn.Input,
		Encoded: bitRatesOut.Encoded,
		Output:  bitRatesOut.Output,
	}, nil
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

func (s *FFStream) onInput(
	ctx context.Context,
	input packetorframefiltercondition.Input,
) bool {
	s.InputQualityMeasurer.ObservePacketOrFrame(ctx, input.Input)
	return true
}

func (s *FFStream) shouldForwardToOutput(
	ctx context.Context,
	input packetorframefiltercondition.Input,
) bool {
	priority, ok1 := avptypes.PipelineSideDataLatest[FallbackPriority](input.Input.GetPipelineSideData())
	idx, ok2 := avptypes.PipelineSideDataLatest[ResourceIndex](input.Input.GetPipelineSideData())
	if !ok1 || !ok2 {
		return true
	}

	// TODO: refactor this, to avoid the locking to decide if a frame/packet should be suppressed.
	s.locker.Lock()
	defer s.locker.Unlock()

	if int(priority) >= len(s.InputsInfo) || int(idx) >= len(s.InputsInfo[priority]) {
		return true
	}

	return !s.InputsInfo[priority][idx].Suppressed
}

func (s *FFStream) onStreamMapped(
	ctx context.Context,
	priority uint,
	resourceIdx ResourceIndex,
	mediaType astiav.MediaType,
	globalIdx int,
) {
	if mediaType != astiav.MediaTypeAudio {
		return
	}

	s.locker.Lock()
	defer s.locker.Unlock()

	key := fallbackResourceKey{Priority: priority, ResourceIdx: resourceIdx}
	if oldIdx, ok := s.audioStreamIndices[key]; ok && oldIdx == globalIdx {
		return
	}
	s.audioStreamIndices[key] = globalIdx
	logger.Infof(ctx, "ffstream: detected audio stream for input (priority=%d, resource=%d) at global index %d", priority, resourceIdx, globalIdx)

	resource := &s.InputsInfo[priority][resourceIdx]

	// Check if this input is a reference for others
	for otherIdx, r := range s.InputsInfo[priority] {
		if r.SyncUsingReferenceAudio != nil && *r.SyncUsingReferenceAudio == int(resourceIdx) {
			// Target input needs this reference. Check if target audio is already known.
			if targetGlobalIdx, ok := s.audioStreamIndices[fallbackResourceKey{Priority: priority, ResourceIdx: ResourceIndex(otherIdx)}]; ok {
				s.audioSync.AddTrackConfig(ctx, targetGlobalIdx, kernel.AudioSyncTrackConfig{
					ReferenceStreamIndex: globalIdx,
					MovingAverageCount:   10,
				})
			}
		}
	}

	// Check if THIS input needs a reference
	if resource.SyncUsingReferenceAudio != nil {
		refResourceIdx := ResourceIndex(*resource.SyncUsingReferenceAudio)
		if refGlobalIdx, ok := s.audioStreamIndices[fallbackResourceKey{Priority: priority, ResourceIdx: refResourceIdx}]; ok {
			s.audioSync.AddTrackConfig(ctx, globalIdx, kernel.AudioSyncTrackConfig{
				ReferenceStreamIndex: refGlobalIdx,
				MovingAverageCount:   10,
			})
		}
	}
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
