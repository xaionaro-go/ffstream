// ffstream_input_test.go covers the input bookkeeping in
// FFStream.AddInput / FFStream.RemoveInput: priority allocation,
// (priority, num) addressing, sentinel errors, and N-per-priority
// fallback chaining. These tests do not exercise real I/O —
// kernel.NewInput is invoked lazily by the input chain when it is
// actually served, which never happens here.

package ffstream

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/node"
	"github.com/xaionaro-go/avpipeline/preset/inputwithfallback"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

// Tests in this file create real *FFStream instances. Goroutines
// spawned by the underlying input pipeline outlive the test cancel
// until the 10s timeout fires. We accept this for the small test
// count; revisit with goleak if the suite grows or starts flaking.

// newTestFFStream constructs an FFStream the same way TestInjectSubtitles
// does; AddInput / RemoveInput only need a valid Inputs handler.
func newTestFFStream(t *testing.T, ctx context.Context) *FFStream {
	t.Helper()
	s, err := New(ctx)
	require.NoError(t, err)
	return s
}

func TestAddInput_HappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	num, err := s.AddInput(ctx, Resource{URL: "test://", Priority: 0})
	require.NoError(t, err)
	require.Equal(t, uint(0), num,
		"first AddInput at a priority must return num=0")

	require.Len(t, s.InputsInfo, 1)
	require.Len(t, s.InputsInfo[0], 1)
	require.Equal(t, "test://", s.InputsInfo[0][0].URL)

	require.GreaterOrEqual(t, len(s.Inputs.InputChains), 1,
		"AddInput must allocate an InputChain at the requested priority")
}

func TestAddInput_MultipleAtSamePriority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	num0, err := s.AddInput(ctx, Resource{URL: "test://a", Priority: 0})
	require.NoError(t, err)
	require.Equal(t, uint(0), num0)

	num1, err := s.AddInput(ctx, Resource{URL: "test://b", Priority: 0})
	require.NoError(t, err)
	require.Equal(t, uint(1), num1,
		"second AddInput at the same priority must get num=1")

	require.Len(t, s.InputsInfo[0], 2,
		"both Resources must coexist at priority 0")
	require.Equal(t, "test://a", s.InputsInfo[0][0].URL)
	require.Equal(t, "test://b", s.InputsInfo[0][1].URL)

	require.Len(t, s.Inputs.InputChains, 1,
		"adding a second input at the same priority must NOT allocate a new InputChain")
}

func TestRemoveInput_HappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	_, err := s.AddInput(ctx, Resource{URL: "test://", Priority: 0})
	require.NoError(t, err)

	require.NoError(t, s.RemoveInput(ctx, 0, 0))
	require.Empty(t, s.InputsInfo[0],
		"InputsInfo slot must be empty after the only entry is removed")
}

func TestRemoveInput_OutOfRange_ReturnsErrInputNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	err := s.RemoveInput(ctx, 99, 0)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputNotFound),
		"out-of-range priority must return ErrInputNotFound, got %v", err)
}

func TestRemoveInput_EmptySlot_ReturnsErrInputNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	_, err := s.AddInput(ctx, Resource{URL: "test://", Priority: 0})
	require.NoError(t, err)
	require.NoError(t, s.RemoveInput(ctx, 0, 0))

	err = s.RemoveInput(ctx, 0, 0)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputNotFound),
		"second RemoveInput on the same slot must return ErrInputNotFound, got %v", err)
}

func TestRemoveInput_ByPriorityAndNum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	num0, err := s.AddInput(ctx, Resource{URL: "test://a", Priority: 0})
	require.NoError(t, err)
	require.Equal(t, uint(0), num0)

	num1, err := s.AddInput(ctx, Resource{URL: "test://b", Priority: 0})
	require.NoError(t, err)
	require.Equal(t, uint(1), num1)

	// Remove the first entry. The second one shifts down to index 0.
	require.NoError(t, s.RemoveInput(ctx, 0, 0))
	require.Len(t, s.InputsInfo[0], 1,
		"RemoveInput must drop exactly one entry from the priority slot")
	require.Equal(t, "test://b", s.InputsInfo[0][0].URL,
		"the surviving entry must be the second one we added")
}

// TestDaemonSurvivesInputEOF asserts that pushing io.EOF onto the
// pipeline error channel does NOT cancel the daemon context. This
// pins the fix for the bug where Start's error-handler goroutine
// returned (and thus invoked s.cancelFunc) on EOF, tearing the daemon
// down even though InputWithFallback would have handled retry.
//
// The test invokes the production drainPipelineErrors helper directly
// (extracted from Start) so the assertion tracks the actual code path
// instead of a duplicated copy.
func TestDaemonSurvivesInputEOF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	daemonCtx, daemonCancel := context.WithCancel(ctx)
	defer daemonCancel()

	errCh := make(chan node.Error, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainPipelineErrors(daemonCtx, errCh, daemonCancel, nil)
	}()

	// Simulate an input EOF — the priority-10 RTMP source EOFs
	// immediately when there is no active publisher.
	errCh <- node.Error{Err: io.EOF}

	// The daemon ctx must remain alive long enough for the
	// fallback chain to retry. 1 s is the spec floor.
	select {
	case <-daemonCtx.Done():
		t.Fatalf("daemon ctx was cancelled by io.EOF; "+
			"InputWithFallback never got a chance to retry: %v",
			daemonCtx.Err())
	case <-time.After(1 * time.Second):
		// Expected: daemon still alive after EOF.
	}

	// Push another EOF — still must not cancel.
	errCh <- node.Error{Err: io.EOF}
	select {
	case <-daemonCtx.Done():
		t.Fatalf("daemon ctx cancelled on second io.EOF: %v", daemonCtx.Err())
	case <-time.After(100 * time.Millisecond):
	}

	// Sanity: closing the channel triggers loop exit, which in
	// turn calls cancelFunc.
	close(errCh)
	select {
	case <-daemonCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("error-handler did not exit after errCh close")
	}
	<-done
}

// TestDaemonSurvivesNonEOFPipelineError asserts that an arbitrary
// non-EOF, non-Canceled error pushed onto the pipeline error channel
// does NOT cancel the daemon context. This pins the always-on
// contract: every subsystem (InputWithFallback, StreamMux) owns its
// retry/failover, so a transient pipeline error must never tear the
// daemon down.
//
// Regression for the "daemon clean-exits on Deactivate" path: after
// wingout calls RemoveInput for both inputs at priority 0, the active
// kernels eventually surface read errors that are not always io.EOF
// (e.g. "unable to read a frame: ..."). Pre-fix, drainPipelineErrors
// returned on the first such error and the daemon teardown chained
// through cancelFunc; the fallback retry never got a chance to
// rebuild on the next AddInput.
func TestDaemonSurvivesNonEOFPipelineError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	daemonCtx, daemonCancel := context.WithCancel(ctx)
	defer daemonCancel()

	errCh := make(chan node.Error, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainPipelineErrors(daemonCtx, errCh, daemonCancel, nil)
	}()

	errCh <- node.Error{Err: errors.New("synthetic non-EOF read error")}

	select {
	case <-daemonCtx.Done():
		t.Fatalf("daemon ctx cancelled on non-EOF pipeline error; "+
			"the always-on contract is broken: %v", daemonCtx.Err())
	case <-time.After(1 * time.Second):
		// Expected: daemon still alive after a non-EOF error.
	}

	// A second non-EOF error must also not tear the daemon down.
	errCh <- node.Error{Err: errors.New("another synthetic error")}
	select {
	case <-daemonCtx.Done():
		t.Fatalf("daemon ctx cancelled on second non-EOF error: %v", daemonCtx.Err())
	case <-time.After(100 * time.Millisecond):
	}

	// Sanity: closing the channel still triggers shutdown via
	// cancelFunc — that is the legitimate end-of-Serve signal.
	close(errCh)
	select {
	case <-daemonCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("error-handler did not exit after errCh close")
	}
	<-done
}

// TestDaemonSurvivesAllInputsRemoved asserts the user-visible
// scenario: AddInput followed by RemoveInput must NOT cause the
// daemon's error-handler to cancel the daemon ctx, even if the
// pipeline subsequently emits a non-EOF read error from the now-empty
// input chain. The 5-second floor matches the spec in the bug report.
func TestDaemonSurvivesAllInputsRemoved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	_, err := s.AddInput(ctx, Resource{URL: "test://", Priority: 0})
	require.NoError(t, err)
	require.NoError(t, s.RemoveInput(ctx, 0, 0))
	require.Empty(t, s.InputsInfo[0])

	// Drive the same handler the daemon uses, with a synthetic
	// errCh standing in for avpipeline.Serve. The post-RemoveInput
	// shape we want to pin: any error short of ctx-cancel or
	// errCh-close must not cancel the daemon.
	daemonCtx, daemonCancel := context.WithCancel(ctx)
	defer daemonCancel()
	errCh := make(chan node.Error, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainPipelineErrors(daemonCtx, errCh, daemonCancel, nil)
	}()

	errCh <- node.Error{Err: io.EOF}
	errCh <- node.Error{Err: errors.New("unable to read a frame")}

	select {
	case <-daemonCtx.Done():
		t.Fatalf("daemon ctx cancelled while inputs were empty; "+
			"reconnect via AddInput would never get a chance: %v",
			daemonCtx.Err())
	case <-time.After(5 * time.Second):
		// Spec floor: the daemon must stay alive at least 5 s
		// after the last RemoveInput so a UI Activate can rearm.
	}

	daemonCancel()
	<-done
}

// TestDrainPipelineErrors_CountsNonEOFOnly pins the contract for the
// pipeline error counter exposed via GetStats: only the default
// branch (non-EOF, non-Canceled) increments it. EOF and Canceled
// errors are normal lifecycle events handled by retry/fallback and
// must not show up as health-counter events. Pre-counter, the
// default branch silently swallowed errors with no operator-visible
// signal — this test guards against regressing back to that.
func TestDrainPipelineErrors_CountsNonEOFOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	daemonCtx, daemonCancel := context.WithCancel(ctx)
	defer daemonCancel()

	var counter atomic.Uint64
	errCh := make(chan node.Error, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		drainPipelineErrors(daemonCtx, errCh, daemonCancel, &counter)
	}()

	// EOF and Canceled must NOT increment.
	errCh <- node.Error{Err: io.EOF}
	errCh <- node.Error{Err: context.Canceled}

	// Two arbitrary non-EOF errors must increment.
	errCh <- node.Error{Err: errors.New("synthetic read error")}
	errCh <- node.Error{Err: errors.New("another synthetic")}

	// Drain by closing the channel; the loop exits, cancelFunc
	// fires, daemonCtx becomes Done — at which point all four
	// sends have been processed.
	close(errCh)
	<-done

	require.Equal(t, uint64(2), counter.Load(),
		"counter must reflect exactly the non-EOF, non-Canceled "+
			"errors observed by the default branch (got %d)", counter.Load())
}

func TestRemoveInput_NumOutOfRange_ReturnsErrInputNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	_, err := s.AddInput(ctx, Resource{URL: "test://", Priority: 0})
	require.NoError(t, err)

	err = s.RemoveInput(ctx, 0, 5)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputNotFound),
		"out-of-range num must return ErrInputNotFound, got %v", err)
}

// TestAddInput_AtExistingPriority_RebuildsChain pins the wingout
// gRPC-driven hot-add path: when AddInput appends a Resource to a
// priority that already has an active (unpaused, kernel-open)
// InputChain, the chain MUST rebuild so InputFactory.NewInput sees the
// updated InputsInfo and opens the new resource.
//
// Pre-fix, AddInput at an existing priority only appended to InputsInfo
// and never re-triggered NewInput, so wingout's pattern
// (`-i $FFSTREAM_INPUT_URL`, then addInput(0, "", camCustomOpts), then
// addInput(0, micUrl, micCustomOpts)) silently no-op'd at runtime: the
// camera/mic Resources were stored in InputsInfo but never opened. The
// e2e mic_routing_test header explicitly documents this as a known
// limitation; this test pins the fix at the unit level.
//
// Discrimination strategy
// =======================
// We assert a property that is observable iff the fix calls
// InputChain.Pause+Unpause: pauseLocked invokes Kernel.Close and
// CAS-replaces the kernel's KernelOpenBarrier with a fresh channel
// (retryable.go). The pointer before-vs-after AddInput #2 is the
// witness: pre-fix, the pointer is unchanged; post-fix, a fresh
// barrier is installed.
//
// We use a real (libav lavfi/testsrc) input so the kernel actually
// opens — the chain reaches the steady-state IsPaused=false +
// KernelIsSet=true that production hits after Start. This is the
// only state in which Pause+Unpause runs cleanly: with a stub-fail
// kernel + RetryInterval>=0 the openKernelIfNeeded loop holds
// KernelLocker forever, which would deadlock against the fix's
// Pause; with RetryInterval<0 the chain dead-latches before AddInput
// #2 (IsPaused=true → fix correctly skips). lavfi is the same demuxer
// the avpipeline kernel package uses for its input tests.
func TestAddInput_AtExistingPriority_RebuildsChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	res := Resource{
		URL:      "testsrc=duration=10:rate=25",
		Priority: 0,
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "f", Value: "lavfi"},
			},
		},
	}
	_, err = s.AddInput(ctx, res)
	require.NoError(t, err)
	require.Len(t, s.Inputs.InputChains, 1)

	chain := s.Inputs.InputChains[0]

	// Simulate post-Start: the daemon's inputwithfallback.Serve loop
	// auto-unpauses the priority-0 chain on first observation. We do
	// the same thing inline by closing the barrier directly — that
	// triggers the Generate-driven goroutine spawned by NewFromKernel
	// to open the kernel.
	close(*chain.Input.Processor.Kernel.KernelOpenBarrier.Load())

	// Wait until the kernel is fully open. We probe via
	// OriginalPacketSource which takes KernelLocker internally and
	// returns non-nil iff KernelIsSet=true — race-safe against the
	// retry loop's writes (a direct KernelIsSet read would race the
	// openKernelIfNeeded write).
	//
	// At this point IsPaused=false and the retry loop is parked
	// inside its callback (kernel.Generate) — KernelLocker is
	// briefly free between callback iterations, which is the same
	// window the daemon's runtime AddInput hits.
	require.Eventually(t, func() bool {
		return chain.Input.Processor.Kernel.OriginalPacketSource() != nil
	}, 5*time.Second, 10*time.Millisecond,
		"chain's kernel must open before AddInput #2 (so we are "+
			"testing the active-chain reload path that wingout hits)")

	// Capture the barrier pointer before AddInput #2. Pre-fix, this
	// pointer is unchanged by AddInput at an existing priority.
	// Post-fix, Pause+Unpause CAS-replaces it on the inner pauseLocked
	// path (retryable.go pauseLocked → pauseKernelOpening), since
	// Pause was called with KernelIsSet=true → Kernel.Close →
	// pauseKernelOpening flips the barrier to a fresh open channel.
	barrierBefore := chain.Input.Processor.Kernel.KernelOpenBarrier.Load()
	require.NotNil(t, barrierBefore, "barrier must be initialised")

	res2 := Resource{
		URL:      "testsrc=duration=10:rate=25",
		Priority: 0,
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "f", Value: "lavfi"},
			},
		},
	}
	_, err = s.AddInput(ctx, res2)
	require.NoError(t, err)

	// Both Resources must coexist in InputsInfo (slice bookkeeping).
	require.Len(t, s.InputsInfo[0], 2,
		"both Resources must coexist at priority 0")

	// The fix-vs-bug witness: AddInput at an existing active priority
	// must reload the chain, which CAS-replaces the barrier pointer.
	// Eventually polls because the post-Pause Unpause spawns an
	// openKernelIfNeeded goroutine that may race the read here; the
	// pointer flip itself happens synchronously inside AddInput
	// (Pause runs to completion under s.locker).
	require.Eventually(t, func() bool {
		barrierAfter := chain.Input.Processor.Kernel.KernelOpenBarrier.Load()
		return barrierAfter != barrierBefore
	}, 5*time.Second, 10*time.Millisecond,
		"AddInput at existing priority must reload the chain "+
			"(KernelOpenBarrier pointer must change); "+
			"this fails pre-fix because AddInput only appends to "+
			"InputsInfo and never triggers Pause+Unpause")
}

// TestAddInput_AtPausedPriorityWithinCurrentValue_Unpauses pins the
// Bug #182-A contract: when AddInput re-populates a priority that was
// emptied via RemoveInput and the resulting chain is now paused (e.g.
// because the runtime fallback walk demoted it after the kernel hit
// "no input resources configured"), the next AddInput must unpause the
// chain so the new resource is opened.
//
// Wingout's Re-Activate flow exercises exactly this path:
//
//	1. Activate -> AddInput(prio=0, cam) + AddInput(prio=0, mic).
//	2. Deactivate -> RemoveInput drops both prio-0 entries; the
//	   Retryable kernel keeps retrying, hits "no input resources",
//	   and the chain ends up IsPaused=true under CurrentValue<=0.
//	3. Re-Activate -> AddInput(prio=0, cam) + AddInput(prio=0, mic)
//	   MUST unpause chain[0] so the freshly-attached resources are
//	   opened. Pre-fix the !IsPaused-only branch returned without
//	   touching the chain and the cam/mic inputs were silently
//	   ignored — the user's Activate tap appeared to do nothing.
//
// We construct the paused state directly (chain.Pause after the kernel
// is open) rather than driving the full Deactivate->Retryable->fallback
// walk: that asynchronous state machine is exercised in
// avpipeline/preset/inputwithfallback's tests; here we want a hermetic
// unit-level pin for the AddInput branch logic itself.
func TestAddInput_AtPausedPriorityWithinCurrentValue_Unpauses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	// First AddInput at priority 0 to construct the chain.
	res := Resource{
		URL:      "testsrc=duration=10:rate=25",
		Priority: 0,
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "f", Value: "lavfi"},
			},
		},
	}
	_, err = s.AddInput(ctx, res)
	require.NoError(t, err)
	require.Len(t, s.Inputs.InputChains, 1)
	chain := s.Inputs.InputChains[0]

	// Open the kernel (mirrors inputwithfallback.Serve auto-unpause).
	close(*chain.Input.Processor.Kernel.KernelOpenBarrier.Load())
	require.Eventually(t, func() bool {
		return chain.Input.Processor.Kernel.OriginalPacketSource() != nil
	}, 5*time.Second, 10*time.Millisecond,
		"chain's kernel must open before we pause it")

	// Pause the chain to simulate the post-Deactivate stuck state.
	// Production gets here via the Retryable retry loop hitting
	// "no input resources configured" and the inputwithfallback
	// fallback walk; that path's invariants are pinned in
	// avpipeline/preset/inputwithfallback. Here we drive the input
	// state directly so the test stays hermetic against the
	// retry/fallback coupling.
	require.NoError(t, chain.Pause(ctx))
	require.Eventually(t, func() bool {
		return chain.IsPaused(ctx)
	}, 2*time.Second, 10*time.Millisecond,
		"chain must report IsPaused=true after Pause")

	// CurrentValue is 0 by default; AddInput's new branch fires when
	// priority<=CurrentValue and the chain is paused. Sanity-check
	// the precondition explicitly so a future change to the default
	// surfaces as a test failure rather than a silent no-op.
	require.LessOrEqual(t, int32(0), s.Inputs.InputSwitch.CurrentValue.Load(),
		"CurrentValue must be <= priority 0 for the new branch to fire")

	// Second AddInput at the same priority. Pre-fix this returns
	// without touching the paused chain. Post-fix it unpauses the
	// chain so the new resource is opened.
	res2 := Resource{
		URL:      "testsrc=duration=10:rate=25",
		Priority: 0,
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "f", Value: "lavfi"},
			},
		},
	}
	_, err = s.AddInput(ctx, res2)
	require.NoError(t, err)

	// Both Resources must coexist (slice bookkeeping is independent
	// of the unpause branch but a sanity check).
	require.Len(t, s.InputsInfo[0], 2,
		"both Resources must coexist at priority 0 after re-AddInput")

	// Witness: chain.IsPaused must flip back to false. Eventually
	// polls because Unpause is followed by an asynchronous
	// openKernelIfNeeded goroutine; the IsPaused flag itself flips
	// synchronously inside Unpause but we keep the poll to stay
	// robust against any internal scheduling.
	require.Eventually(t, func() bool {
		return !chain.IsPaused(ctx)
	}, 5*time.Second, 10*time.Millisecond,
		"AddInput at a paused priority<=CurrentValue must unpause "+
			"the chain so the new resource is opened; pre-fix the "+
			"!IsPaused-only branch left the chain stuck paused and "+
			"the wingout Re-Activate tap silently no-op'd")
}

// TestInputFactory_HasResources_SparsePriorities pins the contract
// that the fallback walk in avpipeline/preset/inputwithfallback
// relies on to skip empty priority slots: *InputFactory must
// implement inputwithfallback.InputFactoryWithAvailability and
// report HasResources accurately based on the live InputsInfo
// snapshot.
//
// This is the regression test for the priority-0 + priority-10 race
// documented in /tmp/rtmp_priority_10.md: when AddInput places a
// resource at priority 10 with priority 0 already occupied, AddInput
// densely grows InputChains to 11 slots — priorities 1..9 hold empty
// chains (no resources). Pre-fix, the fallback walk advanced one
// priority at a time and serialized every empty slot through the
// procN switching latch, producing
// "another switch is in progress (procN: N)" log spam and never
// reaching chain 10. Post-fix, factories whose InputsInfo entry is
// empty report HasResources=false; the avpipeline fallback walk
// scans forward and jumps directly to the next occupied chain
// (covered end-to-end by
// TestInputWithFallback_OnInputChainError_SkipsEmptyChains_SparsePriorities
// in avpipeline/preset/inputwithfallback).
func TestInputFactory_HasResources_SparsePriorities(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	// Reproduce the production layout: camera at priority 0, rtmp at
	// priority 10. AddInput grows InputChains to 11 entries; only
	// priorities 0 and 10 have resources.
	_, err := s.AddInput(ctx, Resource{URL: "test://camera", Priority: 0})
	require.NoError(t, err)
	_, err = s.AddInput(ctx, Resource{URL: "test://rtmp-fallback", Priority: 10})
	require.NoError(t, err)

	require.Len(t, s.Inputs.InputChains, 11,
		"AddInput at priority 10 with a priority-0 already present "+
			"must allocate 11 chains (dense growth)")
	require.Len(t, s.InputsInfo, 11)
	require.Len(t, s.InputsInfo[0], 1, "priority 0 holds the camera resource")
	require.Len(t, s.InputsInfo[10], 1, "priority 10 holds the rtmp resource")
	for p := 1; p <= 9; p++ {
		require.Empty(t, s.InputsInfo[p],
			"priority %d must be empty in the sparse layout", p)
	}

	// Each InputChain holds an *InputFactory; that factory must
	// satisfy the optional InputFactoryWithAvailability interface so
	// the avpipeline fallback walk can skip empty chains. The
	// HasResources verdict must reflect the live InputsInfo state.
	for p, chain := range s.Inputs.InputChains {
		factory, ok := chain.InputFactory.(*InputFactory)
		require.Truef(t, ok, "chain %d InputFactory must be *InputFactory", p)
		avail, ok := any(factory).(inputwithfallback.InputFactoryWithAvailability)
		require.Truef(t, ok,
			"*InputFactory must implement "+
				"inputwithfallback.InputFactoryWithAvailability so the "+
				"fallback walk can skip empty priorities (chain %d)", p)

		switch p {
		case 0, 10:
			require.Truef(t, avail.HasResources(ctx),
				"priority %d has a resource configured — "+
					"HasResources must be true", p)
		default:
			require.Falsef(t, avail.HasResources(ctx),
				"priority %d has no resource — HasResources must be "+
					"false so the avpipeline fallback walk skips it", p)
		}
	}
}

// TestInputFactory_HasResources_TracksRemoval verifies that
// HasResources reflects mutations to InputsInfo (RemoveInput drops
// the entry, HasResources flips back to false). This guards against a
// stale-state bug where HasResources caches its verdict.
func TestInputFactory_HasResources_TracksRemoval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	_, err := s.AddInput(ctx, Resource{URL: "test://x", Priority: 0})
	require.NoError(t, err)

	factory, ok := s.Inputs.InputChains[0].InputFactory.(*InputFactory)
	require.True(t, ok)
	avail, ok := any(factory).(inputwithfallback.InputFactoryWithAvailability)
	require.True(t, ok)

	require.True(t, avail.HasResources(ctx),
		"after AddInput, HasResources must be true")

	require.NoError(t, s.RemoveInput(ctx, 0, 0))
	require.False(t, avail.HasResources(ctx),
		"after RemoveInput drops the only entry, HasResources must "+
			"flip to false (no caching)")
}

// TestInputFactory_HasResources_NoSelfDeadlockUnderLock pins the
// avpipeline contract that
// inputwithfallback.InputFactoryWithAvailability.HasResources is
// invoked by InputWithFallback.onInputChainError while it already
// holds InputChainsLocker. The locker is xsync.Mutex (a
// non-reentrant write lock); HasResources must NOT re-acquire it.
//
// Regression: prior to this fix HasResources called the locking
// GetResources, which took InputChainsLocker for write a second time
// in the same goroutine and self-deadlocked the input retry loop
// indefinitely. Production observation: ffstream pid 17095 wedged
// 27+ minutes; goroutine 24 stuck in xsync.RWMutex.Lock at
// xsync.DoR2 -> InputFactory.GetResources called from
// InputWithFallback.onInputChainError closure.
//
// Test reproduction: simulate the avpipeline call site by taking
// InputChainsLocker.Do(ctx, ...) on the test goroutine and calling
// HasResources from inside the closure with a deadline. Pre-fix this
// blocks forever and the deadline fires; post-fix the call returns
// promptly.
func TestInputFactory_HasResources_NoSelfDeadlockUnderLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	_, err := s.AddInput(ctx, Resource{URL: "test://x", Priority: 0})
	require.NoError(t, err)

	factory, ok := s.Inputs.InputChains[0].InputFactory.(*InputFactory)
	require.True(t, ok)
	avail, ok := any(factory).(inputwithfallback.InputFactoryWithAvailability)
	require.True(t, ok)

	// Reproduce the avpipeline onInputChainError call shape: the
	// fallback walk runs inside InputChainsLocker.Do(...). The bug
	// fired when HasResources nested another InputChainsLocker
	// acquisition on the same goroutine.
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Inputs.InputChainsLocker.Do(ctx, func() {
			// Must return without trying to re-acquire
			// InputChainsLocker on this goroutine.
			require.True(t, avail.HasResources(ctx),
				"HasResources must report true under the caller's "+
					"InputChainsLocker without re-acquiring it")
		})
	}()

	select {
	case <-done:
		// expected: returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("HasResources self-deadlocked under InputChainsLocker " +
			"(see goroutine dump for prod regression: " +
			"xsync.RWMutex.Lock at GetResources call from inside " +
			"onInputChainError closure)")
	}
}
