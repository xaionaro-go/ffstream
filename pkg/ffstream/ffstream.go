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
	barrierstategetter "github.com/xaionaro-go/avpipeline/kernel/barrier/stategetter"
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

	cancelFunc context.CancelFunc
	locker     sync.Mutex

	audioStreamIndices map[fallbackResourceKey]int

	// pipelineErrorCount counts non-EOF, non-Canceled errors observed by drainPipelineErrors. Exposed via GetStats.PipelineErrorCount.
	pipelineErrorCount atomic.Uint64
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
	inputOpts = append(inputOpts, inputwithfallback.OptionQuietOnOpenFailure(cfg.QuietOnOpenFailure))
	inputs, err := inputwithfallback.New[*Input, *DecoderFactory, CustomData](ctx, nil, inputOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to create the inputs handler: %w", err)
	}
	// Enable cross-chain PTS bridging on the InputSwitch when configured.
	// avpipeline's flag defaults OFF; without this set the per-chain PTS
	// offset bridge introduced in avpipeline 500d143 is a no-op and the
	// cross-clock-domain freeze (camera->rtmp ~600s leap on chain switch)
	// is NOT mitigated. ffstream's prod use-case is specifically the cross
	// clock-domain switch, so the default (in DefaultConfig) is true.
	if cfg.BridgePTSAcrossChains {
		inputs.InputSwitch.Flags.Set(barrierstategetter.SwitchFlagBridgePTSAcrossChains)
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
				logger.Errorf(ctx, "pipeline error: %v", err.Err)
				if errorCount != nil {
					errorCount.Add(1)
				}
				continue
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
	// Append the resource to InputsInfo[priority] BEFORE AddFactory so
	// the brand-new-chain path (chainPreExisted=false) sees the resource
	// on its first natural Unpause. AddFactory below sends the new chain
	// on inputwithfallback's newInputChainChan; the receiver loop spawns
	// the chain's Serve goroutine AND immediately calls
	// inputChain.Unpause for any chain at-or-below the active fallback
	// priority. Unpause spawns the chain's Retryable openKernelIfNeeded
	// goroutine which calls the chain Factory's NewInput →
	// GetResources(priority). With the previous ordering (append AFTER
	// AddFactory) the Factory fired with InputsInfo[priority]=[];
	// NewInput returned "no input resources configured for priority N";
	// the Retryable flipped its barrier to paused; the hot-reload kick
	// below (gated on chainPreExisted=true) never ran for the
	// brand-new-chain path so the resource was silently orphaned and
	// Input.Video stayed at 0 forever. The symptom was historically
	// masked by the avpipeline 0630171 "context canceled" wedge (the
	// wedge fired before the race could resolve, so the empty-resources
	// path was never observed in steady state).
	//
	// Append-first is safe under the InputsInfo/InputChains size
	// invariant: GetInputsInfo iterates InputChains and never indexes
	// past it, so a transient state where InputsInfo[priority] has a
	// resource but the chain hasn't been created yet is invisible to
	// concurrent readers. The reverse skew (chain visible,
	// InputsInfo[priority] OOB) would crash GetResources, but doesn't
	// happen here because the InputsInfo's outer slice was already
	// extended up to priority earlier (in the InputChainsLocker.Do
	// block above).
	s.Inputs.InputChainsLocker.Do(ctx, func() {
		s.InputsInfo[priority] = append(s.InputsInfo[priority], resource)
		num = uint(len(s.InputsInfo[priority]) - 1)
	})
	for p := s.Inputs.GetInputChainsCount(ctx); p <= int(priority); p++ {
		// AddFactory takes InputChainsLocker itself; called outside
		// our Do() to avoid deadlock on the non-reentrant mutex.
		// On failure, align InputsInfo back to InputChains so the
		// invariant len(InputsInfo) == len(InputChains) holds AND
		// roll back the resource we just appended above.
		if err := s.Inputs.AddFactory(ctx, newInputFactory(s, uint(p))); err != nil {
			s.Inputs.InputChainsLocker.Do(ctx, func() {
				newCount := len(s.Inputs.InputChains)
				if len(s.InputsInfo) > newCount {
					s.InputsInfo = s.InputsInfo[:newCount]
				}
				for len(s.InputsInfo) < newCount {
					s.InputsInfo = append(s.InputsInfo, nil)
				}
				// Roll back the resource append: AddFactory failed,
				// so the chain at this priority will never exist to
				// consume the resource.
				if int(priority) < len(s.InputsInfo) && len(s.InputsInfo[priority]) > 0 {
					s.InputsInfo[priority] = s.InputsInfo[priority][:len(s.InputsInfo[priority])-1]
				}
			})
			return 0, fmt.Errorf("unable to add input factory at priority %d: %w", p, err)
		}
	}
	// When AddInput appends to a pre-existing
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
	//   - Existing chain at priority<=currentValue but paused (e.g.,
	//     all resources were RemoveInput'd earlier and the chain was
	//     paused as a side-effect of the resulting fallback walk):
	//     unpause now so the new resource is opened. Without this,
	//     re-Activate after Deactivate leaves the chain stuck paused
	//     and the resource is silently ignored. The auto-unpause in
	//     inputwithfallback.Serve only fires when a chain ARRIVES on
	//     the channel — pre-existing chains miss it.
	if chainPreExisted {
		chain := xsync.DoR1(ctx, &s.Inputs.InputChainsLocker, func() *InputChain {
			return s.Inputs.InputChains[priority]
		})
		switch {
		case !chain.IsPaused(ctx) && chain.IsKernelOpen(ctx):
			// Pause+Unpause kick is only safe when the kernel is
			// fully opened (KernelIsSet=true) — not just intent-to-
			// open (barrier flipped, KernelIsSet still false).
			// Pausing a kernel that's mid-Factory closes the
			// freshly-opened resource; for camera2 NDK that means
			// self-eviction of the just-CONNECT'd camera client
			// (root cause: log shows
			//   AddInput camera → "capture session is active"
			//   AddInput mic → chainPreExisted Pause+Unpause kick
			//   → retryable.go:207 "retryable is being closed"
			//   → "concurrent open/close won the race"
			//   → "capture session was closed"
			// then dumpsys media.camera shows pid X EVICTED BY pid X).
			// When the kernel is still opening, the in-flight Factory's
			// GetResources snapshot is stale (the resource we just
			// appended above isn't in it), so the new resource won't
			// be in this open's Tee — but the chain's natural retry
			// cycle will pick it up on the next NewInput call without
			// us tearing down the live session. Trade-off: a second
			// AddInput arriving during the first AddInput's open
			// window is silently deferred to the chain's next retry
			// instead of forcing an immediate reload. That is the
			// correct behaviour for the camera+mic-at-priority-0
			// pattern where both arrive within milliseconds.
			if err := chain.Pause(ctx); err != nil {
				return num, fmt.Errorf("unable to pause input chain at priority %d for hot-reload: %w", priority, err)
			}
			if err := chain.Unpause(ctx); err != nil {
				return num, fmt.Errorf("unable to unpause input chain at priority %d after hot-reload: %w", priority, err)
			}
		case int32(priority) <= s.Inputs.InputSwitch.CurrentValue.Load():
			// Chain is paused but its priority is at-or-above the
			// active fallback. Unpause so the new resource opens —
			// the kernel-open path will request a switch back to
			// this priority via onInputChainKernelOpen if the
			// active fallback is currently a lower-priority chain.
			if err := chain.Unpause(ctx); err != nil {
				return num, fmt.Errorf("unable to unpause input chain at priority %d after hot-add: %w", priority, err)
			}
		}
	}
	// Refresh the streammux RawFrameSource flag so a hot-added camera
	// resource (e.g. wingout's gRPC AddInput at priority 0, which fires
	// after Start) flips the encoder pix_fmt fix on for the upcoming
	// SwitchOutputByProps. The flag is sticky-true: once a raw-frame
	// source has been seen we keep it set even if the source is later
	// removed, so the encoder reconfig that followed the camera-add
	// stays consistent.
	//
	// CAMERA-AFTER-RTMP CAVEAT: SetRawFrameSource ResetHard
	// closes the running encoder so the next frame reopens it with
	// pix_fmt=nv12, but it does NOT drain decoded mediacodec frames the
	// rtmp h264 decoder may have already pushed into the InputFixer →
	// TranscoderNode queue. One transient eviction is therefore expected
	// the first time camera supersedes rtmp; the streammux-side no-
	// sibling recreate path (recommitDemotedInputToSibling →
	// recreateEvictedOutputFunc) materialises a fresh Output under the
	// same SenderKey, whose encoder opens cleanly with pix_fmt=nv12 from
	// the start, and the camera-direct chain then runs steady-state.
	if s.StreamMux != nil && isRawFrameSourceFormat(inputFormatFromResource(resource)) {
		s.StreamMux.SetRawFrameSource(ctx, true)
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
	//
	// LOCK ORDER: the slice-delete + walk runs
	// under InputChainsLocker, but the resulting Pause / Unpause /
	// SetValue calls run OUTSIDE that lock. They take Retryable's
	// KernelLocker (InputChain.Pause/Unpause delegate to
	// Retryable.Pause/Unpause via Input.Processor.Kernel — see
	// preset/inputwithfallback/input_chain.go:257-267 and
	// kernel/retryable.go:444). Retryable.openKernelIfNeeded grabs
	// KernelLocker and then calls Factory → InputFactory.NewInput →
	// GetResources, which takes InputChainsLocker
	// (input_factory.go:170). Holding InputChainsLocker across the
	// Pause/Unpause/SetValue calls would invert that order and risk
	// deadlock against a concurrent kernel-open. Mirroring AddInput's
	// chainPreExisted pattern (xsync.DoR1 → release → Pause/Unpause
	// outside the lock) keeps a single canonical order: KernelLocker →
	// InputChainsLocker.
	type fallbackPlan struct {
		// nextID == -1: no walk action needed (resources still present,
		// non-active priority emptied, or no later priority has
		// resources). The slice-delete still happens.
		nextID       int32
		activeChain  *InputChain
		targetChain  *InputChain
		activePaused bool
		targetPaused bool
	}
	plan := xsync.DoR1(ctx, &s.Inputs.InputChainsLocker, func() fallbackPlan {
		s.InputsInfo[priority] = slices.Delete(s.InputsInfo[priority], int(num), int(num)+1)

		// Symmetric counterpart to AddInput's onInputChainKernelOpen
		// auto-DOWN-switch: if removing the last resource at the
		// currently-active priority leaves that chain unable to open,
		// walk forward to the next priority that still HasResources and
		// flip InputSwitch up to it. Without this, the active chain
		// stays selected with an empty resource list — its NewInput
		// returns "no input resources configured for priority %d" on
		// every retry tick, no kernel ever opens, GetInputsInfo emits
		// an empty list, and downstream stalls (wingout Deactivate
		// emptied prio 0; CurrentValue stayed 0 and the priority-10
		// rtmp fallback was never promoted).
		if len(s.InputsInfo[priority]) != 0 {
			return fallbackPlan{nextID: -1} // resources still present — slice-delete only
		}
		cur := s.Inputs.InputSwitch.CurrentValue.Load()
		if int32(priority) != cur {
			return fallbackPlan{nextID: -1} // emptied a non-active priority — keep CurrentValue
		}
		// Walk forward via the SSOT helper that backs
		// inputwithfallback.onInputChainError. Both walks honour
		// HasResources, so a factory that adds custom availability
		// semantics in the future behaves consistently across
		// error-driven and removal-driven fallback. HasResources is
		// invoked under InputChainsLocker; *InputFactory.HasResources
		// reads via GetResourcesLocked accordingly
		// (input_factory.go:225-241).
		//
		// The helper preserves onInputChainError's exact
		// `!ok`-as-available semantics: a factory that does NOT
		// implement InputFactoryWithAvailability has no availability
		// check, so we treat it as a candidate. Skipping on `!ok`
		// would diverge from the canonical walk and make
		// removal-driven fallback fail for any future factory type
		// that does not opt in to the optional interface.
		nextID := int32(inputwithfallback.WalkAvailableAfter(ctx, s.Inputs.InputChains, int(priority)))
		if nextID < 0 {
			logger.Debugf(ctx, "RemoveInput: no fallback past priority %d; switch stays at %d (chain will idle)", priority, cur)
			return fallbackPlan{nextID: -1}
		}
		// Capture chain pointers + IsPaused under the lock so we can
		// release it before invoking Pause / Unpause / SetValue (which
		// take KernelLocker — see lock-order note above).
		p := fallbackPlan{nextID: nextID}
		if activeChain := s.Inputs.InputChains[priority]; activeChain != nil {
			p.activeChain = activeChain
			p.activePaused = activeChain.IsPaused(ctx)
		}
		if targetChain := s.Inputs.InputChains[nextID]; targetChain != nil {
			p.targetChain = targetChain
			p.targetPaused = targetChain.IsPaused(ctx)
		}
		return p
	})
	if plan.nextID < 0 {
		return nil // slice-delete done; no fallback action required
	}
	// Pause the now-empty chain so its retry loop stops spamming
	// "no input resources configured" while we wait for the fallback.
	// Pause runs OUTSIDE InputChainsLocker (it takes KernelLocker).
	if plan.activeChain != nil && !plan.activePaused {
		if err := plan.activeChain.Pause(ctx); err != nil {
			logger.Errorf(ctx, "RemoveInput: pause active-but-empty chain %d: %v", priority, err)
		}
	}
	// SetValue first — only Unpause the target on success. If SetValue
	// returns ErrSwitchInProgress the OnSwitchRequest goroutine never
	// runs, so an early Unpause would leave the target chain open
	// without the switch pointing at it (it would re-open its kernel,
	// produce packets that never reach the keep-unless predicate, and
	// burn cycles). On success, SetValue's OnSwitchRequest also
	// unpauses [0, nextID] asynchronously
	// (preset/inputwithfallback/input_with_fallback.go:301-327); the
	// explicit Unpause here is belt-and-suspenders so the target opens
	// promptly without waiting for the async goroutine.
	if err := s.Inputs.InputSwitch.SetValue(ctx, plan.nextID); err != nil {
		logger.Errorf(ctx, "RemoveInput: switch to fallback %d failed: %v", plan.nextID, err)
		return nil
	}
	if plan.targetChain != nil && plan.targetPaused {
		if err := plan.targetChain.Unpause(ctx); err != nil {
			logger.Errorf(ctx, "RemoveInput: unpause fallback chain %d: %v", plan.nextID, err)
		}
	}
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
	// IDLE-START contract: when ffstream boots without a CLI output URL,
	// no template was registered. SetOutputURL is the only
	// output-shaping RPC exposed via gRPC, so it doubles as the
	// template-creation entry point in that mode. Multi-template is
	// still unsupported (senderFactory.NewSender requires exactly one).
	switch len(s.OutputTemplates) {
	case 0:
		s.OutputTemplates = append(s.OutputTemplates, SenderTemplate{
			URLTemplate: url,
		})
		return nil
	case 1:
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
	default:
		return fmt.Errorf("at most one output template is supported, got %d", len(s.OutputTemplates))
	}
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
	// IDLE-START contract: the rc.local supervisor model boots
	// ffstream with zero inputs and zero output templates and expects
	// the gRPC layer to drive activation later (wingout's AddInput +
	// SetOutputURL + SwitchOutputByProps on user tap). Zero-state Start
	// is allowed; the senderFactory.NewSender / SwitchOutputByProps
	// guards reject downstream calls that legitimately need an output
	// template. Callers that DO have an output template are still
	// required to register exactly one — multi-template is unsupported
	// (see senderFactory.NewSender).
	if len(s.OutputTemplates) > 1 {
		return fmt.Errorf("at most one output template is supported, got %d", len(s.OutputTemplates))
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
	// Propagate the quiet-on-open-failure flag so the autobitrate
	// handler demotes its by-design "unable to get encoder" log to
	// Debug while no input is flowing.
	s.StreamMux.QuietOnMissingEncoder.Store(s.Config.QuietOnOpenFailure)

	// Declare raw-frame source pipelines (android_camera /
	// android_microphone) to streammux so MediaCodec encoders skip the
	// surface-passthrough trap and open with pix_fmt=nv12. Set
	// once at Start() based on the resources observed at that point;
	// adding a non-raw-frame fallback later (e.g. RTMP) does not
	// regress the camera path because the pix_fmt is fixed when the
	// encoder is first opened, and a SW-tolerant pix_fmt remains valid
	// once a packet-source decoder begins feeding the encoder too.
	//
	// HOT-ADD PRESERVATION: only call SetRawFrameSource when we
	// actually have a raw-frame upstream. A
	// SetRawFrameSource(false) here previously cleared a flag that may
	// have been latched true by an earlier AddInput hot-add (wingout's
	// gRPC AddInput at priority 0 fires before this Start re-reads the
	// resource set in some boot orderings). The avpipeline-side
	// SetRawFrameSource is now a no-op on the false arm (sticky-true
	// contract pinned at the type level via OneWayBool), but the
	// caller-side guard avoids the misleading "false call ignored"
	// debug spam and documents the intent: this code path is only the
	// ARM, never the disarm.
	if s.hasRawFrameSourceInput() {
		s.StreamMux.SetRawFrameSource(ctx, true)
	}

	if err := s.StreamMux.SetAutoBitRateVideoConfig(ctx, autoBitRateVideo); err != nil {
		return fmt.Errorf("unable to set the auto-bitrate config %#+v: %w", autoBitRateVideo, err)
	}

	s.audioSync = kernel.NewAudioSync(ctx, nil)
	syncNode := node.NewFromKernel(ctx, s.audioSync)

	// Observe quality metrics on every input, then split routing by media type:
	// audio frames go through AudioSync (and the gap filler if enabled);
	// non-audio frames bypass AudioSync entirely and go straight to StreamMux.
	//
	// AudioSync.SendInput holds s.locker for the entire
	// call and forwards every frame to outCh under the lock. Routing video and
	// other media through it adds nothing of value (AudioSync only mutates audio
	// PTS) yet contends with audio frames for the locker, and any back-pressure
	// from a downstream consumer pinned the locker indefinitely — wedging audio
	// alongside video. By keeping non-audio off AudioSync we eliminate the
	// contention entirely; the ctx-escape backstop in audio_sync.go remains as a
	// defence-in-depth against future regressions.
	//
	// The s.onInput condition is set on s.Inputs.SetInputFilter so that the
	// inputwithfallback preset can hook it on each inputChain.Filter (a real
	// destination receiving pre-decode packets), where GetInputFilter is
	// consulted per push from inputChain.Input.
	s.Inputs.SetInputFilter(ctx, packetorframefiltercondition.Function(s.onInput))

	audioOnly := packetorframefiltercondition.MediaType(astiav.MediaTypeAudio)
	nonAudio := packetorframefiltercondition.Function(func(
		ctx context.Context,
		in packetorframefiltercondition.Input,
	) bool {
		return in.Input.GetMediaType() != astiav.MediaTypeAudio
	})

	// Audio path: Inputs --(audio)--> syncNode --> [gapFiller -->] StreamMux
	s.Inputs.AddPushTo(ctx, syncNode, audioOnly)
	if enableGapFiller {
		gapCfg := kernel.DefaultGapFillerConfig()
		gapCfg.OverlapStrategyAudio = kernel.OverlapStrategyAudioSpeedUp
		gapFillerNode := node.NewFromKernel(ctx, kernel.NewGapFiller(ctx, &gapCfg))
		syncNode.AddPushTo(ctx, gapFillerNode, packetorframefiltercondition.Function(s.shouldForwardToOutput))
		gapFillerNode.AddPushTo(ctx, s.StreamMux)
	} else {
		syncNode.AddPushTo(ctx, s.StreamMux, packetorframefiltercondition.Function(s.shouldForwardToOutput))
	}

	// Non-audio path: Inputs --(non-audio)--> StreamMux directly (bypasses AudioSync).
	s.Inputs.AddPushTo(ctx, s.StreamMux, nonAudio, packetorframefiltercondition.Function(s.shouldForwardToOutput))

	// IDLE-START contract: if no output template was registered,
	// defer the initial SwitchOutputByProps / autoBitRate preinit to the
	// caller's later RPC (wingout's SetOutputURL + SwitchOutputByProps
	// on Activate). senderFactory.NewSender requires exactly one
	// template; calling SwitchOutputByProps here without one would
	// fatal-fail Start.
	if len(s.OutputTemplates) == 1 {
		if err := s.SwitchOutputByProps(ctx, streammuxtypes.SenderProps{
			TranscoderConfig: transcoderConfig,
			SenderNodeProps:  streammuxtypes.SenderNodeProps{},
		}); err != nil {
			return fmt.Errorf("SwitchOutputByProps(%#+v): %w", transcoderConfig, err)
		}

		if autoBitRateVideo != nil {
			s.preemptivelyInitAutoBitRateOutputs(ctx, transcoderConfig, autoBitRateVideo)
		}
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

// ReinitEncoder triggers an explicit close+reopen of the active video
// encoder. It returns the wall-clock duration of the close+open phase.
//
// This is intended for instrumented canary measurement of the encoder
// reconfig pause and as a building block for future automated tests.
// In production, encoder reinit happens implicitly via SetResolution /
// SetQuality with a codec change; this entry point lets callers
// reproduce the same hot path on demand.
func (s *FFStream) ReinitEncoder(
	ctx context.Context,
) (_dur time.Duration, _err error) {
	logger.Debugf(ctx, "ReinitEncoder")
	defer func() { logger.Debugf(ctx, "/ReinitEncoder: %v %v", _dur, _err) }()

	s.locker.Lock()
	defer s.locker.Unlock()
	if s.StreamMux == nil {
		return 0, fmt.Errorf("it is allowed to use ReinitEncoder only after Start is invoked")
	}

	encoderV, _ := s.StreamMux.GetEncoders(ctx)
	if encoderV == nil {
		return 0, fmt.Errorf("no active video encoder")
	}

	reiniter, ok := encoderV.(codec.EncoderReiniter)
	if !ok {
		return 0, fmt.Errorf("active video encoder %T does not support reinit (likely a copy/raw encoder)", encoderV)
	}

	start := time.Now()
	if err := reiniter.Reinit(ctx); err != nil {
		return 0, fmt.Errorf("encoder reinit failed: %w", err)
	}
	return time.Since(start), nil
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

	s.withInputChainsLocker(ctx, func() {
		s.InputsInfo[priority][num].Suppressed = suppressed
	})
	return nil
}

// withInputChainsLocker runs fn under FFStream.Inputs.InputChainsLocker
// when Inputs is non-nil. The nil-Inputs branch lets unit tests
// fabricate a partial FFStream literal (`&FFStream{InputsInfo: ...}`)
// without re-routing every test through the full New(ctx) constructor.
//
// Production code paths always reach this through New(ctx) (or its
// SDK callers), so Inputs is always non-nil there. Mutators that
// touch InputsInfo[...] field-level state (Suppressed,
// CustomOptions) MUST go through this helper so the write is
// synchronized against InputFactory.GetResources readers — those
// readers take InputChainsLocker (and only InputChainsLocker), so an
// unprotected field write would race the slices.Clone in
// getResourcesLocked.
//
// Lock order: callers MUST already hold FFStream.locker; this helper
// then acquires InputChainsLocker on top. That matches AddInput's
// outer s.locker → AddFactory → InputChainsLocker chain.
func (s *FFStream) withInputChainsLocker(ctx context.Context, fn func()) {
	if s.Inputs == nil {
		fn()
		return
	}
	s.Inputs.InputChainsLocker.Do(ctx, fn)
}

// SetInputCustomOption updates a CustomOptions entry on the resource at
// (priority, num). The mutation is performed under FFStream.locker (to
// serialize with sibling FFStream-state writers) and InputChainsLocker
// (to synchronize against InputFactory readers that consume
// CustomOptions from the kernel-open goroutine — hot-add via
// AddInput's Pause+Unpause spawns a fresh InputFactory.NewInput
// goroutine that iterates res.CustomOptions; without InputChainsLocker
// the SetFirst write races slices.Clone in getResourcesLocked).
//
// Lock order matches AddInput: s.locker → InputChainsLocker.
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

	s.withInputChainsLocker(ctx, func() {
		s.InputsInfo[priority][num].CustomOptions.SetFirst(avptypes.DictionaryItem{
			Key:   key,
			Value: value,
		})
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
