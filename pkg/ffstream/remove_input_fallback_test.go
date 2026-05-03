// remove_input_fallback_test.go pins the auto-fallback path of
// FFStream.RemoveInput: when removing the last resource at the active
// priority, the active InputSwitch must flip forward to the next
// priority that still HasResources, so the
// downstream chain has a live source. Pre-fix, the wingout
// Deactivate flow emptied priority 0 and CurrentValue stayed pinned
// to 0 — the priority-10 rtmp fallback never picked up the stream
// even though its resource was configured.
//
// Witness selection: InputWithFallback.New invokes initSwitches,
// which calls InputSwitch.SetKeepUnless with a non-nil keep-unless
// condition. With KeepUnless wired, Switch.SetValue takes the
// setNextValueNow path: it updates NextValue (atomic), and the
// commit to CurrentValue only fires when a real keyframe packet
// reaches the switch's keep-unless predicate. Our tests exercise
// the API surface in isolation (no Start, no packets), so the
// observable signal is NextValue, which is the value SetValue
// stores. CurrentValue stays at 0 until packets flow — that is by
// design and orthogonal to the auto-fallback decision tree this
// suite pins.

package ffstream

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/preset/inputwithfallback"
)

// noAvailabilityFactory implements the base inputwithfallback.InputFactory
// interface but deliberately does NOT implement
// inputwithfallback.InputFactoryWithAvailability. It is the witness the
// "RemoveInput walk SSOT" pin needs: the canonical onInputChainError walk in
// avpipeline/preset/inputwithfallback/input_with_fallback.go
// treats a factory that does NOT implement the optional availability
// interface as "available" (the type-assertion `!ok` branch falls
// through to `nextID = candidate; break`), and FFStream.RemoveInput's
// fallback walk must mirror that semantics line-for-line. None of the
// methods are expected to be invoked by this test — RemoveInput's walk
// inspects only chain.InputFactory's type, never calls NewInput /
// NewDecoderFactory.
type noAvailabilityFactory struct {
	name string
}

var _ inputwithfallback.InputFactory[*Input, *DecoderFactory, CustomData] = (*noAvailabilityFactory)(nil)

func (f *noAvailabilityFactory) String() string { return f.name }

func (f *noAvailabilityFactory) NewInput(
	ctx context.Context,
	chain *InputChain,
) (*Input, error) {
	return nil, fmt.Errorf("noAvailabilityFactory.NewInput must not be called by the RemoveInput walk")
}

func (f *noAvailabilityFactory) NewDecoderFactory(
	ctx context.Context,
	chain *InputChain,
) (*DecoderFactory, error) {
	return nil, fmt.Errorf("noAvailabilityFactory.NewDecoderFactory must not be called by the RemoveInput walk")
}

// TestRemoveInput_AutoFallsBackToNextPriority is the headline pin for
// the auto-fallback path. It is the falsifiable witness: comment out
// the symmetric-fallback block in
// FFStream.RemoveInput (return early after the slice delete) and
// this test fails because NextValue stays at math.MinInt32 (the
// initSwitches sentinel for "no requested switch").
func TestRemoveInput_AutoFallsBackToNextPriority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	// Reproduce the production layout: camera-style resource at
	// priority 0, rtmp-style fallback at priority 10. AddInput
	// densely grows InputChains to 11 entries; only priorities 0 and
	// 10 hold resources.
	_, err := s.AddInput(ctx, Resource{URL: "test://camera", Priority: 0})
	require.NoError(t, err)
	_, err = s.AddInput(ctx, Resource{URL: "test://rtmp-fallback", Priority: 10})
	require.NoError(t, err)

	// Sanity: CurrentValue is 0 (atomic.Int32 zero value); NextValue
	// is the math.MinInt32 sentinel set by NewSwitch — no switch
	// has been requested yet. That matches the production-Deactivate
	// precondition: the camera at priority 0 is active when
	// RemoveInput runs.
	require.Equal(t, int32(0), s.Inputs.InputSwitch.CurrentValue.Load(),
		"precondition: CurrentValue must be 0 (camera priority active)")
	require.Equal(t, int32(math.MinInt32), s.Inputs.InputSwitch.NextValue.Load(),
		"precondition: NextValue must be MinInt32 (no switch requested)")

	// Drop the only entry at priority 0. Post-fix this triggers the
	// auto-fallback walk to priority 10 via Switch.SetValue.
	require.NoError(t, s.RemoveInput(ctx, 0, 0))
	require.Empty(t, s.InputsInfo[0],
		"slice-delete must run regardless of fallback outcome")

	// Witness: NextValue must flip to 10 (the SetValue request lands
	// in setNextValueNow because KeepUnless is wired; CurrentValue
	// commit waits for a real keyframe packet, which never flows in
	// this hermetic test). Eventually because OnSwitchRequest may
	// run on a goroutine and the SetValue commit-mutex serialises
	// with it.
	require.Eventually(t, func() bool {
		return s.Inputs.InputSwitch.NextValue.Load() == 10
	}, 2*time.Second, 10*time.Millisecond,
		"RemoveInput on the last resource at the active priority "+
			"must invoke InputSwitch.SetValue(10); pre-fix "+
			"NextValue stays at MinInt32 because no SetValue is called")

	// Witness #2: the fallback chain at priority 10 must NOT be paused
	// after the switch, otherwise NewInput is never called and the
	// rtmp resource is never opened. The auto-fallback block unpauses
	// it explicitly; SetValue's OnSwitchRequest also unpauses [0,10]
	// — both paths converge on Unpause for chain 10. Eventually polls
	// because OnSwitchRequest's Unpause runs on a goroutine.
	require.Eventually(t, func() bool {
		return !s.Inputs.InputChains[10].IsPaused(ctx)
	}, 2*time.Second, 10*time.Millisecond,
		"fallback chain at priority 10 must be unpaused after the "+
			"switch so its NewInput goroutine can open the rtmp resource")
}

// TestRemoveInput_NonActivePriority_DoesNotSwitch confirms the
// "emptied a non-active priority" guard: removing the last entry at
// priority 5 while priority 0 is the active chain MUST NOT request a
// switch. Without this guard the fix would aggressively switch every
// time any priority emptied — surprising users who merely cleaned up
// an inactive fallback slot.
func TestRemoveInput_NonActivePriority_DoesNotSwitch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	_, err := s.AddInput(ctx, Resource{URL: "test://primary", Priority: 0})
	require.NoError(t, err)
	_, err = s.AddInput(ctx, Resource{URL: "test://fallback-5", Priority: 5})
	require.NoError(t, err)

	require.Equal(t, int32(0), s.Inputs.InputSwitch.CurrentValue.Load(),
		"precondition: CurrentValue must be 0")
	require.Equal(t, int32(math.MinInt32), s.Inputs.InputSwitch.NextValue.Load(),
		"precondition: NextValue must be MinInt32 (no switch requested)")

	// Remove the only entry at priority 5 (NOT the active one).
	require.NoError(t, s.RemoveInput(ctx, 5, 0))
	require.Empty(t, s.InputsInfo[5])

	// NextValue must remain pinned at MinInt32: the auto-fallback
	// guard `int32(priority) != cur` is what keeps cleanup of
	// inactive slots from churning the switch. Never polls so a
	// late-firing SetValue would also be caught.
	require.Never(t, func() bool {
		return s.Inputs.InputSwitch.NextValue.Load() != math.MinInt32
	}, 200*time.Millisecond, 20*time.Millisecond,
		"emptying a non-active priority must NOT call SetValue; "+
			"the auto-fallback guard `int32(priority) != cur` is what "+
			"keeps cleanup of inactive slots from churning the switch")
}

// TestRemoveInput_NoFallbackAvailable_StaysOnActive confirms the
// "no fallback past priority %d" path: when the active priority
// empties and no later priority has resources, no SetValue is
// invoked (we do not point InputSwitch at a non-existent fallback).
func TestRemoveInput_NoFallbackAvailable_StaysOnActive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	// Only priority 0 is populated. There is no later priority.
	_, err := s.AddInput(ctx, Resource{URL: "test://only", Priority: 0})
	require.NoError(t, err)
	require.Equal(t, int32(0), s.Inputs.InputSwitch.CurrentValue.Load())
	require.Equal(t, int32(math.MinInt32), s.Inputs.InputSwitch.NextValue.Load())

	require.NoError(t, s.RemoveInput(ctx, 0, 0))
	require.Empty(t, s.InputsInfo[0])

	// The walk hits no candidate; SetValue is never called and
	// NextValue stays at the MinInt32 sentinel. The auto-fallback
	// block's `if nextID < 0 { return }` is the witness.
	require.Never(t, func() bool {
		return s.Inputs.InputSwitch.NextValue.Load() != math.MinInt32
	}, 200*time.Millisecond, 20*time.Millisecond,
		"with no fallback past the active priority, SetValue must "+
			"NOT be called; the auto-fallback block's "+
			"`if nextID < 0 { return }` is the witness")
}

// TestRemoveInput_NoLockOrderInversion is the falsification test for
// the lock-order fix.
//
// The canonical lock order in this codebase is KernelLocker →
// InputChainsLocker: Retryable.openKernelIfNeeded grabs KernelLocker
// (kernel/retryable.go:183) and then calls Factory →
// InputFactory.NewInput → GetResources, which takes
// InputChainsLocker (input_factory.go:170).
//
// Pre-fix RemoveInput inverted that order — it held
// InputChainsLocker.Do(...) across chain.Pause(ctx), and InputChain.Pause
// delegates to Retryable.Pause which takes KernelLocker
// (preset/inputwithfallback/input_chain.go:263-267 +
// kernel/retryable.go:447). InputChainsLocker → KernelLocker is the
// inverse. With one goroutine in the canonical order (KernelLocker
// held, waiting for InputChainsLocker) and another in the inverse
// order (InputChainsLocker held, waiting for KernelLocker), a
// circular wait deadlock is possible.
//
// This test sets up the deadlock structurally:
//
//  1. Manually acquire the active chain's Retryable.KernelLocker —
//     simulating the openKernelIfNeeded "KernelLocker held while
//     entering Factory" hot path.
//  2. Call RemoveInput in a goroutine. Pre-fix, RemoveInput's
//     auto-fallback block synchronously calls activeChain.Pause(ctx)
//     while holding InputChainsLocker — Pause needs KernelLocker
//     (held by us), so it blocks. Whoever holds InputChainsLocker
//     blocks on KernelLocker; whoever holds KernelLocker would block
//     on InputChainsLocker — classic AB-BA deadlock.
//  3. RemoveInput releases InputChainsLocker BEFORE calling
//     Pause. The Pause then waits cleanly on the test goroutine's
//     KernelLocker hold; releasing the hold lets RemoveInput finish.
//     The auto-fallback completes within the bounded budget.
//
// The bounded budget of 1.5s is far below the test's 10s top-level
// timeout: it gives a falsifiable witness rather than relying on the
// outer timeout to flag the regression.
func TestRemoveInput_NoLockOrderInversion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use the default FFStream config (InputRetryInterval = -1,
	// "retries disabled" at the inputwithfallback layer — see
	// option.go and
	// preset/inputwithfallback/input_with_fallback.go:571). With
	// retries disabled the natural onInputChainError path bails
	// before its own fallback walk fires, which keeps the
	// procN-latch (input_with_fallback.go:290-293) free of
	// concurrent SetValue contention. Otherwise the 10 empty
	// chains created by AddInput-priority-10 each spin
	// independently calling SetValue(10) via their own retry
	// goroutines and the outcome of the auto-fallback witness
	// becomes flaky on procN ErrSwitchInProgress.
	s := newTestFFStream(t, ctx)

	_, err := s.AddInput(ctx, Resource{URL: "test://camera", Priority: 0})
	require.NoError(t, err)
	_, err = s.AddInput(ctx, Resource{URL: "test://rtmp-fallback", Priority: 10})
	require.NoError(t, err)

	// Manually grab the active chain's KernelLocker and hold it.
	// This blocks any concurrent Retryable open goroutine from
	// taking KernelLocker (and therefore from running
	// Factory→GetResources which would take InputChainsLocker).
	// Holding it ALSO blocks any chain.Pause call — which is
	// exactly the call pre-fix RemoveInput makes while still
	// holding InputChainsLocker.
	activeRetryable := s.Inputs.InputChains[0].Input.Processor.Kernel
	require.True(t, activeRetryable.KernelLocker.ManualTryLock(ctx),
		"precondition: the active chain's KernelLocker must be "+
			"available initially (chain 0 is paused; no kernel-open "+
			"goroutine has been spawned)")
	kernelHeld := true
	defer func() {
		if kernelHeld {
			activeRetryable.KernelLocker.ManualUnlock(ctx)
		}
	}()

	// Now unpause chain 0 so the pre-fix `if !activeChain.IsPaused(ctx)`
	// guard does NOT short-circuit the chain.Pause call site.
	// Retryable.Unpause closes the barrier synchronously
	// (IsPaused=false now) and spawns an open goroutine that
	// blocks on KernelLocker (held by us). So no other goroutine
	// will sneak InputChainsLocker contention in via the open
	// path; the deadlock observation depends purely on RemoveInput's
	// own behavior.
	require.NoError(t, s.Inputs.InputChains[0].Unpause(ctx))
	require.Eventually(t, func() bool {
		return !s.Inputs.InputChains[0].IsPaused(ctx)
	}, 1*time.Second, 10*time.Millisecond,
		"chain 0 must observe IsPaused=false after Unpause so RemoveInput's "+
			"`if !IsPaused -> Pause` branch is reachable")

	// Kick off RemoveInput. Pre-fix: it grabs InputChainsLocker
	// then blocks indefinitely on chain.Pause(ctx) → KernelLocker
	// (held by us). Post-fix: it grabs InputChainsLocker briefly,
	// releases it, then blocks on chain.Pause → KernelLocker
	// (still held by us). The discriminating witness is whether a
	// third-party InputChainsLocker grab succeeds while
	// RemoveInput is mid-flight.
	removeDone := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		removeDone <- s.RemoveInput(ctx, 0, 0)
	}()

	// Probe: can a third party acquire InputChainsLocker while
	// RemoveInput is mid-flight? We give RemoveInput a small head
	// start so it has actually entered its main code path, then
	// race for the lock with a bounded budget.
	time.Sleep(50 * time.Millisecond)
	probeDone := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Inputs.InputChainsLocker.Do(context.Background(), func() {})
		close(probeDone)
	}()

	// Post-fix: probe completes quickly (RemoveInput released
	// InputChainsLocker before blocking on KernelLocker). Pre-fix:
	// probe is wedged because RemoveInput still holds
	// InputChainsLocker waiting on KernelLocker.
	select {
	case <-probeDone:
		// Post-fix path. Now release KernelLocker so RemoveInput
		// can complete its Pause+SetValue+Unpause.
		activeRetryable.KernelLocker.ManualUnlock(ctx)
		kernelHeld = false
		select {
		case err := <-removeDone:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			wg.Wait()
			t.Fatalf("RemoveInput failed to complete after KernelLocker " +
				"was released — should be a quick Pause+SetValue+Unpause path")
		}
	case <-time.After(1 * time.Second):
		// Pre-fix path. Releasing KernelLocker unblocks both
		// RemoveInput and the probe goroutine — then fail.
		activeRetryable.KernelLocker.ManualUnlock(ctx)
		kernelHeld = false
		wg.Wait()
		t.Fatalf("Third-party InputChainsLocker.Do was wedged for 1 s " +
			"while RemoveInput was mid-flight blocked on KernelLocker. " +
			"Lock-order inversion regression: RemoveInput " +
			"held InputChainsLocker across chain.Pause(ctx) which needs " +
			"KernelLocker. Mirror AddInput's xsync.DoR1-then-Pause-outside " +
			"pattern (ffstream.go:310-321).")
	}
	wg.Wait()

	require.Empty(t, s.InputsInfo[0])
	require.Eventually(t, func() bool {
		return s.Inputs.InputSwitch.NextValue.Load() == 10
	}, 2*time.Second, 10*time.Millisecond,
		"auto-fallback to priority 10 must still complete after the lock-release refactor")
}

// TestRemoveInput_AutoFallsBackToFactoryWithoutAvailabilityCheck pins
// the SSOT alignment: when the candidate chain's InputFactory does NOT
// implement
// inputwithfallback.InputFactoryWithAvailability, RemoveInput's
// fallback walk must select that chain as the next switch target —
// NOT skip it. This mirrors onInputChainError's canonical walk in
// preset/inputwithfallback/input_with_fallback.go:610-615:
//
//	if avail, ok := any(chain.InputFactory).(InputFactoryWithAvailability); ok {
//	    if !avail.HasResources(ctx) {
//	        continue  // skip ONLY when implements AND reports false
//	    }
//	}
//	nextID = candidate  // !ok falls through to "available"
//	break
//
// Pre-fix RemoveInput diverged: a `!ok` candidate was skipped, so any
// future factory type that does not opt in to the optional availability
// interface would silently break removal-driven fallback even though
// error-driven fallback worked. Aligning the two walks removes that
// latent regression surface.
//
// Test setup
// ==========
// 1. AddInput at priority 0 (real chain, real *InputFactory — implements
//    availability and reports HasResources=true).
// 2. AddInput at priority 1 (real chain — needed so InputChains[1] is a
//    valid, fully-wired chain that can be Pause/Unpause'd).
// 3. Replace InputChains[1].InputFactory with the
//    noAvailabilityFactory mock (defined in this file). The mock
//    deliberately does NOT implement InputFactoryWithAvailability, so
//    the type-assertion at the walk site lands in the `!ok` branch.
//    Replacing only the InputFactory field is safe: the kernel's
//    Retryable closed over the original factory at construction time
//    (input_chain.go:100-103), so production code paths that need the
//    real factory continue to use it; only the walk's direct read of
//    chain.InputFactory observes the mock.
// 4. RemoveInput at priority 0 — drops the only resource at the active
//    priority, triggering the auto-fallback walk.
//
// Witness
// =======
// Pre-fix (walk: `if !ok { continue }`): walk visits chain 1,
// type-asserts to InputFactoryWithAvailability, fails, skips. No more
// chains; nextID stays -1; SetValue is never called; NextValue stays at
// math.MinInt32.
//
// Post-fix (walk: `if ok && !avail.HasResources(ctx) { continue }`):
// walk visits chain 1, type-asserts fails, falls through to
// `nextID = 1; break`. SetValue(1) is invoked; NextValue flips to 1.
//
// The require.Eventually polling NextValue==1 is the falsifiable
// witness: revert the fix in ffstream.go's walk and this test
// fails because NextValue stays pinned at MinInt32.
func TestRemoveInput_AutoFallsBackToFactoryWithoutAvailabilityCheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	_, err := s.AddInput(ctx, Resource{URL: "test://camera", Priority: 0})
	require.NoError(t, err)
	_, err = s.AddInput(ctx, Resource{URL: "test://fallback", Priority: 1})
	require.NoError(t, err)

	require.Len(t, s.Inputs.InputChains, 2,
		"AddInput at priorities 0 and 1 must allocate exactly 2 chains")

	// Swap chain 1's InputFactory with a mock that does NOT implement
	// inputwithfallback.InputFactoryWithAvailability. The walk reads
	// chain.InputFactory directly; the kernel's Retryable already
	// captured the original factory in a closure at construction time,
	// so production code that opens kernels keeps using the real
	// factory. We do the swap under InputChainsLocker because the walk
	// reads the same field under the same lock — keeping the write
	// observable to the walk's read on every architecture.
	s.Inputs.InputChainsLocker.Do(ctx, func() {
		s.Inputs.InputChains[1].InputFactory = &noAvailabilityFactory{name: "mock-no-availability"}
	})

	// Pre-condition sanity: the swapped factory really fails the type
	// assertion the walk makes. If this passes pre-fix, the test would
	// not actually exercise the `!ok` branch.
	_, ok := any(s.Inputs.InputChains[1].InputFactory).(inputwithfallback.InputFactoryWithAvailability)
	require.False(t, ok,
		"precondition: the swapped factory must NOT implement "+
			"InputFactoryWithAvailability so the walk hits the `!ok` "+
			"branch — the very semantics this test pins")

	require.Equal(t, int32(0), s.Inputs.InputSwitch.CurrentValue.Load(),
		"precondition: CurrentValue must be 0 (priority-0 chain active)")
	require.Equal(t, int32(math.MinInt32), s.Inputs.InputSwitch.NextValue.Load(),
		"precondition: NextValue must be MinInt32 (no switch requested yet)")

	// Drop the only entry at priority 0. Post-fix this triggers the
	// auto-fallback walk; chain 1's mock factory is `!ok` against
	// InputFactoryWithAvailability and (per onInputChainError SSOT)
	// must be selected as the next switch target.
	require.NoError(t, s.RemoveInput(ctx, 0, 0))
	require.Empty(t, s.InputsInfo[0],
		"slice-delete must run regardless of fallback walk outcome")

	// Witness: NextValue must flip to 1. Pre-fix the walk
	// skipped chain 1 on `!ok`, nextID stayed -1, SetValue was never
	// called, NextValue stayed at MinInt32 — that is the falsification
	// signal. require.Never below double-checks the pin is dual-sided:
	// not just "NextValue==1 eventually" but also "NextValue does not
	// later drift away from 1".
	require.Eventually(t, func() bool {
		return s.Inputs.InputSwitch.NextValue.Load() == 1
	}, 2*time.Second, 10*time.Millisecond,
		"RemoveInput walk must select chain 1 even when its "+
			"InputFactory does not implement "+
			"InputFactoryWithAvailability — mirror onInputChainError "+
			"SSOT (input_with_fallback.go:610-615). Pre-fix "+
			"`if !ok { continue }` left NextValue at MinInt32.")

	// Dual-sided: NextValue must NOT drift away from 1 (e.g. back to
	// MinInt32 or any other value). A regression that fired SetValue
	// twice or that some other goroutine reset NextValue would be
	// caught here.
	require.Never(t, func() bool {
		v := s.Inputs.InputSwitch.NextValue.Load()
		return v != 1
	}, 200*time.Millisecond, 20*time.Millisecond,
		"NextValue must remain pinned at 1 after the auto-fallback "+
			"walk selects chain 1; any drift indicates a duplicate "+
			"SetValue or an unexpected reset")
}
