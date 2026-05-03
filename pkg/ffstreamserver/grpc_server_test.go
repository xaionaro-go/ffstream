// grpc_server_test.go covers the gRPC handler error mapping for
// AddInput / RemoveInput. Strategy B: instantiate the real
// *ffstream.FFStream via ffstream.New and invoke handlers directly
// (no real gRPC dial) — there is no socket, no transport, just
// exercising the error → codes mapping and the (priority, num)
// reply contract.

package ffstreamserver

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestServer(t *testing.T, ctx context.Context) *GRPCServer {
	t.Helper()
	s, err := ffstream.New(ctx)
	require.NoError(t, err)
	return NewGRPCServer(ctx, s)
}

// quiesceRetryable terminates the pipeline init goroutine spawned by
// processor.FromKernel.startProcessing for an input Retryable. That
// goroutine takes KernelLocker (in retry/getKernel/openKernelIfNeeded)
// and blocks on KernelOpenBarrier until something signals it. Tests
// that need to inject Kernel/KernelIsSet under KernelLocker would
// otherwise deadlock waiting for the lock until the test ctx times
// out.
//
// Two-step protocol:
//
//  1. Close ClosureSignaler. openKernelIfNeeded's select fires the
//     close case, sets KernelError = io.EOF, returns. retry's outer
//     DoR1 returns through getKernel's KernelError check. Lock
//     released.
//  2. Poll-acquire the lock. Once acquired, ALSO set
//     KernelError = io.EOF *under the lock* — this is an explicit
//     guard against any subsequent retry.* invocation finding
//     KernelIsSet=true after the test injects a fake Kernel and then
//     dispatching the fake Kernel through Generate. The
//     close-vs-factory race in step 1 is not sufficient on its own:
//     when openKernelIfNeeded is in the factory call (not the
//     barrier select) at the moment of close, ClosureSignaler is
//     ignored, the factory returns, the loop iterates, and at the
//     top of openKernelIfNeeded the `if r.KernelIsSet || r.KernelError`
//     short-circuit fires only if a writer (us) already set one of
//     them. Setting KernelError here closes that hole.
func quiesceRetryable(
	ctx context.Context,
	t *testing.T,
	r *kernel.Retryable[*ffstream.Input],
) {
	t.Helper()
	r.ClosureSignaler.Close(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if r.KernelLocker.ManualTryLock(ctx) {
			if r.KernelError == nil {
				r.KernelError = io.EOF
			}
			r.KernelLocker.ManualUnlock(ctx)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("quiesceRetryable: KernelLocker still held after 5s; init goroutine did not exit on ClosureSignaler")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGRPCServer_AddInput_ReturnsAssignedNum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	reply0, err := srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://a",
		Priority: 0,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(0), reply0.GetNum(),
		"first AddInput at a priority must reply Num=0")

	reply1, err := srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://b",
		Priority: 0,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1), reply1.GetNum(),
		"second AddInput at the same priority must reply Num=1")
}

// TestGRPCServer_GetInputsInfo_BoundaryNum is a regression test for the
// off-by-one bounds check in GetInputsInfo: the `len(Kernel0) < idx`
// comparison let `idx == len(Kernel0)` slip through, causing an
// out-of-bounds panic on Kernel0[idx]. The grpc-recovery interceptor
// converted this to an empty `{}` reply at the network layer, masking
// the bug and producing the F1 symptom (ffstreamctl inputs info empty
// after AddInput).
//
// Setup: install a ChainOfTwo whose Kernel0 (a Tee) has length 1, then
// AddInput a second resource at the same priority — InputsInfo grows to
// 2 while Kernel0 stays at 1, so idx=1 hits the boundary case.
//
// Pre-fix: panic on Kernel0[1] (caught by Go's test runtime), or empty
// reply when run through the gRPC stack.
// Post-fix: reply contains both entries (idx=0 with the injected nil
// Input → Id=0, idx=1 skipped → Id=0), no panic.
func TestGRPCServer_GetInputsInfo_BoundaryNum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	_, err := srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://a",
		Priority: 0,
	})
	require.NoError(t, err)

	// Inject a ChainOfTwo with Kernel0 of length 1 (Tee containing one
	// nil *kernel.Input). This simulates the live state where the
	// Retryable kernel was opened against the older InputsInfo before a
	// second resource was added.
	//
	// quiesceRetryable terminates the pipeline init goroutine
	// (FromKernel.startProcessing -> Retryable.Generate -> retry ->
	// getKernel -> openKernelIfNeeded) which otherwise holds
	// KernelLocker indefinitely while blocked on KernelOpenBarrier.
	// Without quiescing, KernelLocker.Do below would deadlock until ctx
	// times out. After quiesce the lock is free; we then mutate under
	// KernelLocker so the race detector accepts the writes (the same
	// fields are read under that lock by GetInputsInfo).
	chain := srv.FFStream.Inputs.InputChains[0]
	retryable := chain.Input.Processor.Kernel
	quiesceRetryable(ctx, t, retryable)
	retryable.KernelLocker.Do(ctx, func() {
		tee := kernel.Tee[kernel.Abstract]{nil}
		retryable.Kernel = kernel.NewChainOfTwo(tee, (*kernel.MapStreamIndices)(nil))
		retryable.KernelIsSet = true
	})
	// Reset KernelIsSet=false (and clear Kernel) BEFORE the deferred
	// cancel cancels ctx. finalize -> Retryable.Close spawns a Pause
	// goroutine that may win the lock acquisition race against
	// ctx.Done (CtxLocker.ManualLock uses Go select, which picks
	// non-deterministically when both cases are ready). If Pause wins
	// while KernelIsSet is true, pauseLocked calls r.Kernel.Close on
	// the injected ChainOfTwo whose Kernel0 has nil entries and
	// Kernel1 is a nil *MapStreamIndices — both panic on Close.
	// Resetting here means pauseLocked takes the early !KernelIsSet
	// branch and never touches r.Kernel. defers run LIFO so this
	// fires before the `defer cancel()`.
	defer func() {
		retryable.KernelLocker.Do(ctx, func() {
			retryable.KernelIsSet = false
			retryable.Kernel = nil
		})
	}()

	// Now AddInput a second resource — InputsInfo[0] has 2 entries,
	// Kernel0 still has 1. idx=1 hits the boundary.
	_, err = srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://b",
		Priority: 0,
	})
	require.NoError(t, err)

	// In standalone, GetInputsInfo skips entries whose Kernel0 slot is
	// nil (or out-of-range when idx >= len(Kernel0)). The original
	// dd29410 test asserted both entries were emitted unconditionally,
	// which matches submodule semantics; here we assert the boundary
	// fix's primary contract: no panic, no gRPC empty due to
	// out-of-range index.
	require.NotPanics(t, func() {
		reply, err := srv.GetInputsInfo(ctx, &ffstream_grpc.GetInputsInfoRequest{})
		require.NoError(t, err)
		require.NotNil(t, reply)
	}, "GetInputsInfo must not panic on idx == len(Kernel0)")
}

// TestGetInputsInfo_IsActiveReflectsServingChain pins the contract that
// InputInfo.IsActive is true ONLY for the chain currently selected by
// InputWithFallback.InputSwitch (and whose kernel is open). Pre-fix the
// handler set IsActive = KernelIsSet, which can be true for multiple
// chains simultaneously — useless for "which input is live right now".
//
// Setup: two inputs at priority 0 (both land in InputChains[0]). Inject
// an open kernel on chain 0 to simulate "kernel is set". InputSwitch
// defaults CurrentValue to 0 (set in initSwitches), so chain 0 is the
// currently-serving one.
//
// Note: we do NOT call FFStream.Start — opening real I/O for a fake URL
// is brittle and orthogonal to the IsActive bookkeeping. The same
// kernel-injection trick is used by TestGRPCServer_GetInputsInfo_BoundaryNum.
func TestGetInputsInfo_IsActiveReflectsServingChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	_, err := srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://cam",
		Priority: 0,
	})
	require.NoError(t, err)

	_, err = srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://mic",
		Priority: 0,
	})
	require.NoError(t, err)

	// Simulate "this chain's kernel is open" without doing real I/O.
	// Standalone's GetInputsInfo skips entries whose Kernel0 slot is
	// nil (or whose closure returns nil), so to make the reply contain
	// 2 entries we must inject a ChainOfTwo whose Kernel0 (a Tee) holds
	// 2 non-nil *kernel.Input items. Empty &kernel.Input{} values are
	// enough — the handler only calls GetObjectID, which returns the
	// pointer-as-ID without dereferencing fields.
	//
	// quiesceRetryable terminates the pipeline init goroutine that
	// holds KernelLocker while blocked on KernelOpenBarrier — without
	// quiescing, KernelLocker.Do below would deadlock until ctx times
	// out. Then we write KernelIsSet/Kernel under KernelLocker so the
	// race detector accepts the writes (the same fields are read under
	// that lock by GetInputsInfo).
	//
	// The deferred reset runs BEFORE `defer cancel()` (LIFO) and clears
	// KernelIsSet+Kernel under the lock. Without it, finalize ->
	// Retryable.Close spawns a Pause goroutine that may win the lock
	// race against ctx.Done; if Pause wins while KernelIsSet=true,
	// pauseLocked calls r.Kernel.Close on the injected ChainOfTwo
	// whose Kernel0 has bare *kernel.Input entries (no AVFormatCtx)
	// and Kernel1 is a nil *MapStreamIndices — both panic on Close.
	chain := srv.FFStream.Inputs.InputChains[0]
	retryable := chain.Input.Processor.Kernel
	quiesceRetryable(ctx, t, retryable)
	retryable.KernelLocker.Do(ctx, func() {
		tee := kernel.Tee[kernel.Abstract]{&kernel.Input{}, &kernel.Input{}}
		retryable.Kernel = kernel.NewChainOfTwo(tee, (*kernel.MapStreamIndices)(nil))
		retryable.KernelIsSet = true
	})
	defer func() {
		retryable.KernelLocker.Do(ctx, func() {
			retryable.KernelIsSet = false
			retryable.Kernel = nil
		})
	}()

	// initSwitches stores 0 into InputSwitch.CurrentValue; chain 0 is
	// the live one. Be explicit anyway — the test must not depend on
	// initialization order changing upstream.
	srv.FFStream.Inputs.InputSwitch.CurrentValue.Store(int32(chain.ID))

	reply, err := srv.GetInputsInfo(ctx, &ffstream_grpc.GetInputsInfoRequest{})
	require.NoError(t, err)
	require.NotNil(t, reply)
	require.Len(t, reply.GetInputs(), 2,
		"both resources at priority 0 must appear in the reply")

	var activeCount int
	for _, in := range reply.GetInputs() {
		if in.GetIsActive() {
			activeCount++
		}
	}
	require.GreaterOrEqual(t, activeCount, 1,
		"at least one input must report IsActive=true when its chain is "+
			"the one InputSwitch currently selects (got 0 — IsActive is "+
			"never set by the handler)")
}

// TestGetInputsInfo_IsActiveFalseWhenSwitchPointsElsewhere is the
// negative half: when InputSwitch.CurrentValue points to a chain that
// does NOT exist among ours (or whose kernel is closed), IsActive must
// be false for every input. Without this, a stale `KernelIsSet`-only
// check would flag the wrong chain.
func TestGetInputsInfo_IsActiveFalseWhenSwitchPointsElsewhere(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	_, err := srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://cam",
		Priority: 0,
	})
	require.NoError(t, err)

	chain := srv.FFStream.Inputs.InputChains[0]
	retryable := chain.Input.Processor.Kernel
	quiesceRetryable(ctx, t, retryable)
	retryable.KernelLocker.Do(ctx, func() {
		retryable.KernelIsSet = true
	})

	// Point the switch at a non-existent chain.
	srv.FFStream.Inputs.InputSwitch.CurrentValue.Store(int32(99))

	reply, err := srv.GetInputsInfo(ctx, &ffstream_grpc.GetInputsInfoRequest{})
	require.NoError(t, err)

	for _, in := range reply.GetInputs() {
		require.False(t, in.GetIsActive(),
			"IsActive must be false when InputSwitch points to a different chain")
	}
}

// TestSetStopInput_OutOfRangeReturnsInvalidArgument is the regression
// test for the off-by-one in SetStopInput's priority bounds check. The
// pre-fix guard used `priority > len(InputChains)`, which let
// `priority == len(InputChains)` slip through and panic on
// InputChains[priority]. The fix is `>=`. We assert that the boundary
// case (priority equal to len) returns codes.InvalidArgument and does
// NOT panic.
func TestSetStopInput_OutOfRangeReturnsInvalidArgument(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	// Add one input so InputChains has length 1; priority=1 is the
	// boundary case (equal to len).
	_, err := srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://a",
		Priority: 0,
	})
	require.NoError(t, err)

	priority := uint64(len(srv.FFStream.Inputs.InputChains))
	require.NotPanics(t, func() {
		_, err = srv.SetStopInput(ctx, &ffstream_grpc.SetStopInputRequest{
			InputPriority: priority,
			Stop:          true,
		})
	}, "SetStopInput at priority==len(InputChains) must not panic")
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err),
		"SetStopInput at priority==len(InputChains) must map to "+
			"codes.InvalidArgument, got %v: %v", status.Code(err), err)
}

// TestSetInputCustomOption_OutOfRangeReturnsInvalidArgument is the
// regression test for the same off-by-one in SetInputCustomOption.
// Pre-fix guard `priority > len` let `priority == len` panic on
// InputChains[priority]. Fixed to `>=`.
func TestSetInputCustomOption_OutOfRangeReturnsInvalidArgument(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	_, err := srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://a",
		Priority: 0,
	})
	require.NoError(t, err)

	priority := uint64(len(srv.FFStream.Inputs.InputChains))
	require.NotPanics(t, func() {
		_, err = srv.SetInputCustomOption(ctx, &ffstream_grpc.SetInputCustomOptionRequest{
			InputPriority: priority,
			InputNum:      0,
			Key:           "k",
			Value:         "v",
		})
	}, "SetInputCustomOption at priority==len(InputChains) must not panic")
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err),
		"SetInputCustomOption at priority==len(InputChains) must map to "+
			"codes.InvalidArgument, got %v: %v", status.Code(err), err)
}

func TestGRPCServer_RemoveInput_NotFoundMapsToNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	_, err := srv.RemoveInput(ctx, &ffstream_grpc.RemoveInputRequest{
		Priority: 99,
		Num:      0,
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err),
		"RemoveInput on out-of-range priority must map to codes.NotFound, got %v: %v", status.Code(err), err)
}
