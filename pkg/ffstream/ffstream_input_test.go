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
	"github.com/xaionaro-go/avpipeline/node"
)

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
