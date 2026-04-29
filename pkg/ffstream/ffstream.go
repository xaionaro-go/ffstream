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
	"slices"
	"sync"
	"sync/atomic"
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
	"github.com/xaionaro-go/xsync"
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

	// pipelineErrorCount tracks non-EOF / non-Canceled errors that
	// drainPipelineErrors observes since process start. Exported via
	// GetStats so the operator can monitor pipeline health without
	// having to scrape logs. Atomic so the always-on error-handler
	// goroutine can increment it without contending the daemon
	// locker.
	pipelineErrorCount atomic.Uint64

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

// drainPipelineErrors runs the Start-time error handler loop for the
// avpipeline.Serve goroutine. Pulled out of Start so the policy can be
// regression-tested in isolation (see TestDaemonSurvivesInputEOF and
// TestDaemonSurvivesAllInputsRemoved).
//
// Policy: the daemon is always-on by design. Lifecycle is owned by the
// daemon ctx (external shutdown) and by avpipeline.Serve naturally
// returning (errCh closed). Pipeline errors are advisory — every
// subsystem owns its own retry/failover:
//   - InputWithFallback retries inputs on EOF/EIO and falls back to
//     lower-priority chains.
//   - StreamMux re-establishes outputs on transient sender failures.
//
// Treating any single error as fatal would tear the daemon down on
// every transient input drop (RTMP publisher disconnect, mic/cam
// resource release on Deactivate, network glitch) and defeat that
// retry. The error-handler therefore logs and continues; only the two
// terminal signals end the loop and cancel the daemon ctx:
//   - ctx done       → the caller already cancelled us.
//   - errCh closed   → avpipeline.Serve returned, so there is nothing
//     left to drive. Closing happens via the deferred close in Start's
//     `avpipeline.Serve` goroutine — i.e., only when ctx propagates
//     cancellation through the Serve tree.
//
// cancelFunc is invoked exactly once on return so the daemon ctx is
// torn down deterministically when the loop exits. Callers MUST hold
// the daemon-ctx cancel as the sole authoritative shutdown lever.
//
// errorCount, if non-nil, is incremented for every observed error
// that falls into the default ("non-EOF, non-Canceled") bucket. This
// is the operator-visible health counter exposed via GetStats.
// Counted under "things the operator may want to investigate" —
// errors we deliberately swallowed in service of the always-on
// contract. Trace-level cancellations and EOFs (handled by retry/
// fallback) are NOT counted: they are normal lifecycle events.
func drainPipelineErrors(
	ctx context.Context,
	errCh <-chan node.Error,
	cancelFunc context.CancelFunc,
	errorCount *atomic.Uint64,
) {
	defer cancelFunc()
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-errCh:
			if !ok {
				logger.Debugf(ctx, "the error channel is closed")
				return
			}
			// node.Error.Error() dereferences err.Node, which can be
			// nil in unit-test fixtures and benign reporters. Format
			// only the wrapped error to keep logs panic-free.
			switch {
			case errors.Is(err.Err, context.Canceled):
				// Cancellation is already being handled by whoever
				// triggered it; surface for trace-level debugging only.
				logger.Debugf(ctx, "cancelled: %v", err.Err)
			case errors.Is(err.Err, io.EOF):
				logger.Debugf(ctx, "input EOF (handled by InputWithFallback): %v", err.Err)
			default:
				// Non-EOF errors used to terminate the daemon. They
				// don't anymore — see the function comment. We log at
				// Warn so the operator still sees them but the
				// always-on contract holds (e.g. RemoveInput on the
				// last active input no longer races a non-EOF read
				// error into a daemon teardown).
				if errorCount != nil {
					errorCount.Add(1)
				}
				logger.Warnf(ctx, "pipeline error (continuing — daemon is always-on): %v", err.Err)
			}
		}
	}
}

func (s *FFStream) AddInput(
	ctx context.Context,
	resource Resource,
) (_num uint, _err error) {
	logger.Debugf(ctx, "AddInput(ctx, %#+v)", resource)
	defer func() { logger.Debugf(ctx, "/AddInput(ctx, %#+v): %d %v", resource, _num, _err) }()
	s.locker.Lock()
	defer s.locker.Unlock()

	// InputsInfo and InputChains MUST grow as a single observation to
	// concurrent GetInputsInfo readers (which iterate InputChains and
	// then dereference InputsInfo[priority] via InputFactory.GetResources).
	// Both reader and writer participate in InputChainsLocker — but
	// AddFactory below acquires that same non-reentrant lock internally,
	// so we cannot just hold it across the whole region. We therefore
	// extend InputsInfo BEFORE growing InputChains: a transient state
	// where InputsInfo has more entries than InputChains is harmless
	// (GetInputsInfo iterates InputChains and never indexes past it),
	// while the reverse skew (chain visible, InputsInfo[priority] OOB)
	// would crash GetResources. The final per-priority append happens
	// under InputChainsLocker so a chain-visible read sees the new
	// resource as soon as the chain is.
	priority := resource.GetFallbackPriority()
	var num uint
	s.Inputs.InputChainsLocker.Do(ctx, func() {
		if len(s.Inputs.InputChains) != len(s.InputsInfo) {
			_err = fmt.Errorf("internal error: len(s.Inputs.InputChains) != len(s.InputsInfo): %d != %d", len(s.Inputs.InputChains), len(s.InputsInfo))
			return
		}
		for p := len(s.Inputs.InputChains); p <= int(priority); p++ {
			s.InputsInfo = append(s.InputsInfo, nil)
		}
	})
	if _err != nil {
		return 0, _err
	}
	// Capture whether the chain at this priority pre-existed before
	// AddFactory (below). A pre-existing chain holds an open kernel
	// constructed from the OLD InputsInfo[priority] snapshot — it
	// will not pick up the new resource we are about to append unless
	// we trigger Retryable.Pause+Unpause to force a fresh
	// InputFactory.NewInput on the live InputsInfo. AddFactory may
	// extend InputChains for higher priorities; those are freshly
	// created and intentionally paused, so they do not need the
	// reload kick.
	chainPreExisted := xsync.DoR1(ctx, &s.Inputs.InputChainsLocker, func() bool {
		return int(priority) < len(s.Inputs.InputChains)
	})
	for p := s.Inputs.GetInputChainsCount(ctx); p <= int(priority); p++ {
		// AddFactory takes InputChainsLocker itself; called outside
		// our Do() to avoid deadlock on the non-reentrant mutex.
		// On failure, align InputsInfo back to InputChains so the
		// invariant len(InputsInfo) == len(InputChains) holds.
		if err := s.Inputs.AddFactory(ctx, newInputFactory(s, uint(p))); err != nil {
			s.Inputs.InputChainsLocker.Do(ctx, func() {
				newCount := len(s.Inputs.InputChains)
				if len(s.InputsInfo) > newCount {
					s.InputsInfo = s.InputsInfo[:newCount]
				}
				for len(s.InputsInfo) < newCount {
					s.InputsInfo = append(s.InputsInfo, nil)
				}
			})
			return 0, fmt.Errorf("unable to add input factory at priority %d: %w", p, err)
		}
	}
	s.Inputs.InputChainsLocker.Do(ctx, func() {
		s.InputsInfo[priority] = append(s.InputsInfo[priority], resource)
		num = uint(len(s.InputsInfo[priority]) - 1)
	})
	// Bug fix (task #90): when AddInput appends to a pre-existing
	// priority slot, the chain's Retryable kernel is already open
	// (or in retry) with the OLD InputsInfo snapshot. Retryable
	// closes-over the resource list at NewInput call time, so a
	// silent slice append never reaches the live kernel. Pause +
	// Unpause is the established mechanism (also used by the
	// fallback-switch and on transient input errors) that closes
	// the in-flight kernel and triggers a fresh
	// Retryable.openKernelIfNeeded → InputFactory.NewInput call,
	// which re-reads InputsInfo[priority] and now opens both the
	// old and the new resource.
	//
	// Guard with !IsPaused so we only reload chains that were
	// actively running:
	//   - Brand-new chain (just created above): IsPaused=true,
	//     skip — its first NewInput will read the up-to-date
	//     InputsInfo when it is naturally unpaused (priority 0
	//     auto-unpause in inputwithfallback.Serve, or the
	//     InputSwitch promoting a fallback).
	//   - Existing chain at priority>0 that hasn't been promoted
	//     to active yet: IsPaused=true, skip — same reasoning;
	//     force-unpausing here would prematurely open a fallback.
	//   - Existing chain serving traffic (or in the retry loop
	//     after a transient error): IsPaused=false, reload now.
	if chainPreExisted {
		chain := xsync.DoR1(ctx, &s.Inputs.InputChainsLocker, func() *InputChain {
			return s.Inputs.InputChains[priority]
		})
		if !chain.IsPaused(ctx) {
			if err := chain.Pause(ctx); err != nil {
				return num, fmt.Errorf("unable to pause input chain at priority %d for hot-reload: %w", priority, err)
			}
			if err := chain.Unpause(ctx); err != nil {
				return num, fmt.Errorf("unable to unpause input chain at priority %d after hot-reload: %w", priority, err)
			}
		}
	}
	return num, nil
}

func (s *FFStream) RemoveInput(
	ctx context.Context,
	priority uint,
	num uint,
) (_err error) {
	logger.Debugf(ctx, "RemoveInput(ctx, %d, %d)", priority, num)
	defer func() { logger.Debugf(ctx, "/RemoveInput(ctx, %d, %d): %v", priority, num, _err) }()
	s.locker.Lock()
	defer s.locker.Unlock()

	if int(priority) >= len(s.InputsInfo) || int(num) >= len(s.InputsInfo[priority]) {
		return ErrInputNotFound
	}

	// N inputs share one InputChain; pausing the chain would stop them all.
	// The factory's GetResources reads InputsInfo[priority] on each NewInput
	// reconstruction, so removing the slice entry is sufficient — the
	// removed Resource will be skipped on the next chain reload.
	//
	// We mutate the slice under InputChainsLocker so that concurrent
	// NewInput → GetResources reads (driven by the retryable kernel
	// goroutine) observe a consistent snapshot — pair the writer with
	// the same lock the reader takes.
	s.Inputs.InputChainsLocker.Do(ctx, func() {
		s.InputsInfo[priority] = slices.Delete(s.InputsInfo[priority], int(num), int(num)+1)
	})
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

// SetOutputURL replaces the URL of the single output template. The
// updated URL is read by the next senderFactory.NewSender call, which
// is triggered by SwitchOutputByProps. Callers wanting an immediate
// effect should call SwitchOutputByProps right after.
//
// Any sticky `-f`/`-format` muxer override left in the template's
// Options is stripped: when callers switch the URL at runtime, the
// new URL's scheme determines the muxer (rtmp/srt/udp/...), and a
// boot-time `-f null` override (used by the launcher when the daemon
// boots without an external sink) would otherwise shadow it and the
// muxer would stay `null`. There is no runtime API to add CustomOptions
// today, so any `-f`/`-format` present here came from boot-time CLI
// args and is by definition stale once the URL changes.
func (s *FFStream) SetOutputURL(
	ctx context.Context,
	url string,
) (_err error) {
	logger.Debugf(ctx, "SetOutputURL(ctx, %q)", url)
	defer func() { logger.Debugf(ctx, "/SetOutputURL(ctx, %q): %v", url, _err) }()
	s.locker.Lock()
	defer s.locker.Unlock()
	if len(s.OutputTemplates) != 1 {
		return fmt.Errorf("exactly one output template is required, got %d", len(s.OutputTemplates))
	}
	s.OutputTemplates[0].URLTemplate = url
	s.OutputTemplates[0].Options = slices.DeleteFunc(
		s.OutputTemplates[0].Options,
		func(item avptypes.DictionaryItem) bool {
			// The CLI parser strips the leading `-` (see
			// convertUnknownOptionsToAVPCustomOptions in cmd/ffstream),
			// so `-f` lands as Key=="f" and `-format` as Key=="format".
			return item.Key == "f" || item.Key == "format"
		},
	)
	return nil
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
	s.locker.Lock()
	defer s.locker.Unlock()
	r := &ffstream_grpc.GetStatsReply{
		NodeCounters: &avpipeline_grpc.NodeCounters{
			Received:  &avpipeline_grpc.NodeCountersSection{},
			Processed: &avpipeline_grpc.NodeCountersSection{},
			Missed:    &avpipeline_grpc.NodeCountersSection{},
			Generated: &avpipeline_grpc.NodeCountersSection{},
			Sent:      &avpipeline_grpc.NodeCountersSection{},
		},
		PipelineErrorCount: s.pipelineErrorCount.Load(),
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
	if s == nil {
		return nil
	}
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.StreamMux == nil {
		return nil
	}
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
	// The s.onInput condition is set on s.Inputs.SetInputFilter so that the
	// inputwithfallback preset can hook it on each inputChain.Filter (a real
	// destination receiving pre-decode packets), where GetInputFilter is
	// consulted per push from inputChain.Input.
	s.Inputs.SetInputFilter(ctx, packetorframefiltercondition.Function(s.onInput))
	s.Inputs.AddPushTo(ctx, syncNode)

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
			EachNode: node.ServeConfig{
				FrameDropVideo: s.Config.FrameDropVideo,
				FrameDropAudio: s.Config.FrameDropAudio,
				FrameDropOther: s.Config.FrameDropOther,
			},
		}, errCh, []node.Abstract{s.Inputs}...)
	})

	observability.Go(ctx, func(ctx context.Context) {
		drainPipelineErrors(ctx, errCh, s.cancelFunc, &s.pipelineErrorCount)
	})

	err = s.StreamMux.WaitForStart(ctx)
	if err != nil {
		return fmt.Errorf("unable to wait for streammux's start: %w", err)
	}

	return nil
}

// injectSubtitlesSendTimeout bounds the wait for the streammux input
// channel to accept the subtitle packet. Sized to absorb very brief
// scheduling hiccups without permitting goroutine pile-up on a
// genuinely starved chain (silent source → downstream blocked →
// InputCh full). See InjectSubtitles for the saturation path this
// guards against.
const injectSubtitlesSendTimeout = 100 * time.Millisecond

// boundedSendInputUnion forwards item to ch with a hard upper bound
// on how long it will wait for the channel to accept. Returns:
//   - nil on success;
//   - ctx.Err() if ctx is canceled before send completes;
//   - ErrPipelineBusy if timeout elapses before send completes.
//
// On every non-success exit, abortCleanup is invoked exactly once so
// the caller can release C-owned resources (packet pool entry, codec
// parameters, etc.) that would otherwise have transferred ownership
// to the consumer with the InputUnion.
//
// This helper is split from InjectSubtitles so the bounded-send
// behaviour is unit-testable in isolation: tests pass a private
// reader-less channel and observe the timeout deterministically,
// without touching streammux internals (which would race with the
// FromKernel reader goroutine).
func boundedSendInputUnion(
	ctx context.Context,
	ch chan<- packetorframe.InputUnion,
	item packetorframe.InputUnion,
	timeout time.Duration,
	abortCleanup func(),
) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		abortCleanup()
		return ctx.Err()
	case ch <- item:
		return nil
	case <-timer.C:
		abortCleanup()
		return ErrPipelineBusy
	}
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
	// Do not free pkt here, it will be freed by the pipeline.

	if err := pkt.AllocPayload(len(data)); err != nil {
		packet.Pool.Put(pkt)
		return fmt.Errorf("unable to allocate payload for subtitle packet: %w", err)
	}
	copy(pkt.Data(), data)

	// Use the last seen audio DTS as the base for PTS/DTS
	dtsNanos := s.StreamMux.Measurements[astiav.MediaTypeAudio].InputDTS.Load()
	if dtsNanos == 0 {
		dtsNanos = s.StreamMux.Measurements[astiav.MediaTypeVideo].InputDTS.Load()
	}
	dtsMs := int64(dtsNanos) / int64(time.Millisecond)

	pkt.SetPts(dtsMs)
	pkt.SetDts(dtsMs)
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

	dstCounters := s.StreamMux.InputAll.Node.GetCountersPtr()
	pktSize := uint64(inputPkt.GetSize())
	pktMediaType := avptypes.MediaType(inputPkt.GetMediaType())
	dstCounters.Addressed.Increment(avptypes.CountersSubSectionIDPackets, pktMediaType, pktSize)

	// Bounded send: if the streammux InputChan cannot accept the
	// packet within injectSubtitlesSendTimeout, treat the pipeline
	// as busy and surface ErrPipelineBusy to the caller.
	//
	// Why this matters: a chronically silent / disconnected source
	// causes the InputAll reader to back-pressure on its downstream
	// (the chain is starved waiting for upstream packets). The
	// cap-1 InputChan saturates almost immediately. A 1 Hz QML
	// poller (injectDiagnostics in Dashboard.qml) would then stack
	// blocked goroutines on the gRPC server, one per RPC handler,
	// until HTTP/2's per-connection concurrent-stream limit was hit
	// and new RPCs returned "EOF preface" / "Deadline exceeded" —
	// even though the daemon is otherwise healthy. Failing the
	// individual call lets the caller back off gracefully and keeps
	// the gRPC connection's stream slots free.
	//
	// On the abort path the consumer never sees the InputUnion, so
	// we own the packet (return to the pool) and the codec
	// parameters (Free the C-allocated struct) and account it as
	// Missed for the operator-visible counter.
	err := boundedSendInputUnion(
		ctx,
		s.StreamMux.InputChan(),
		packetorframe.InputUnion{Packet: &inputPkt},
		injectSubtitlesSendTimeout,
		func() {
			dstCounters.Missed.Increment(avptypes.CountersSubSectionIDPackets, pktMediaType, pktSize)
			packet.Pool.Put(pkt)
			streamInfo.CodecParameters.Free()
		},
	)
	if err != nil {
		return err
	}
	dstCounters.Received.Increment(avptypes.CountersSubSectionIDPackets, pktMediaType, pktSize)
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
	dtsNanos := s.StreamMux.Measurements[astiav.MediaTypeAudio].InputDTS.Load()
	if dtsNanos == 0 {
		dtsNanos = s.StreamMux.Measurements[astiav.MediaTypeVideo].InputDTS.Load()
	}
	dtsMs := int64(dtsNanos) / int64(time.Millisecond)

	pkt.SetPts(dtsMs)
	pkt.SetDts(dtsMs)
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

	dstCounters := s.StreamMux.InputAll.Node.GetCountersPtr()
	pktSize := uint64(inputPkt.GetSize())
	pktMediaType := avptypes.MediaType(inputPkt.GetMediaType())
	dstCounters.Addressed.Increment(avptypes.CountersSubSectionIDPackets, pktMediaType, pktSize)

	// Bounded send: same back-pressure rationale as InjectSubtitles
	// (see comments there). Surface ErrPipelineBusy on chan-saturated
	// stalls so the caller fails fast instead of stacking goroutines.
	err := boundedSendInputUnion(
		ctx,
		s.StreamMux.InputChan(),
		packetorframe.InputUnion{Packet: &inputPkt},
		injectSubtitlesSendTimeout,
		func() {
			dstCounters.Missed.Increment(avptypes.CountersSubSectionIDPackets, pktMediaType, pktSize)
			packet.Pool.Put(pkt)
			streamInfo.CodecParameters.Free()
		},
	)
	if err != nil {
		return err
	}
	dstCounters.Received.Increment(avptypes.CountersSubSectionIDPackets, pktMediaType, pktSize)
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
	// The downstream reduceframerate filter asserts Den >= Num (fraction <= 1.0):
	// see avpipeline/packetorframe/filter/reduceframerate/reduce_framerate_fraction.go.
	// Reject fractions greater than 1.0 here to surface a clean error instead of
	// tripping the filter's assertion at runtime.
	if num > den {
		return fmt.Errorf("fraction must be <= 1.0 (num <= den), got %d/%d", num, den)
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

// SetInputCustomOption updates a CustomOptions entry on the resource at
// (priority, num). The mutation is performed under FFStream.locker so it
// does not race with AddInput and other readers that snapshot the slice
// while holding the same lock.
func (s *FFStream) SetInputCustomOption(
	ctx context.Context,
	priority uint,
	num ResourceIndex,
	key string,
	value string,
) error {
	s.locker.Lock()
	defer s.locker.Unlock()

	if int(priority) >= len(s.InputsInfo) {
		return fmt.Errorf("input priority %d is out of range", priority)
	}
	if int(num) < 0 || int(num) >= len(s.InputsInfo[priority]) {
		return fmt.Errorf("input num %d is out of range at priority %d", num, priority)
	}

	s.InputsInfo[priority][num].CustomOptions.SetFirst(avptypes.DictionaryItem{
		Key:   key,
		Value: value,
	})
	return nil
}

// SnapshotInputsInfo returns a deep-copy snapshot of InputsInfo taken under
// FFStream.locker. The returned slice and its nested Resources do not alias
// the live InputsInfo; concurrent mutation by AddInput / SetSuppressed /
// SetInputCustomOption after this call does not affect the snapshot.
//
// Callers that ALSO need InputChains data must take this snapshot BEFORE
// acquiring Inputs.InputChainsLocker, because AddInput holds FFStream.locker
// while calling AddFactory which in turn acquires InputChainsLocker. Taking
// the locks in the reverse order (InputChainsLocker, then FFStream.locker)
// would deadlock.
func (s *FFStream) SnapshotInputsInfo(ctx context.Context) []Resources {
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.InputsInfo == nil {
		return nil
	}
	out := make([]Resources, len(s.InputsInfo))
	for i, rs := range s.InputsInfo {
		out[i] = rs.Clone()
	}
	return out
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

	if int(priority) >= len(s.InputsInfo) || int(resourceIdx) >= len(s.InputsInfo[priority]) {
		return
	}
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
