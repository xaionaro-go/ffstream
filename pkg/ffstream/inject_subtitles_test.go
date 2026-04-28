package ffstream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/packetorframe"
	"github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
)

func TestInjectSubtitles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	s.StreamMux, err = streammux.NewWithCustomData[CustomData](ctx, streammuxtypes.MuxModeSameOutputSameTracks, nil)
	require.NoError(t, err)

	err = s.InjectSubtitles(ctx, []byte("Hello World"), time.Second)
	require.NoError(t, err)
}

// TestBoundedSendInputUnion_ErrPipelineBusyOnFullChan is the
// regression test for task #104: the bounded-send helper that
// InjectSubtitles delegates to must NOT block forever when the
// destination channel cannot accept the item. It must time out and
// return ErrPipelineBusy, and must invoke the abort cleanup exactly
// once so the caller's C-owned resources (packet pool entry, codec
// parameters) are released.
//
// In production this path triggers when the streammux InputChan is
// full because the chain is starved (silent upstream → downstream
// back-pressures → cap-1 chan saturates). Without the timeout, QML's
// 1 Hz injectDiagnostics poller would stack blocked goroutines on
// the gRPC server until HTTP/2's per-connection concurrent-stream
// limit was hit and new RPCs erred "EOF preface" / "Deadline
// exceeded".
//
// We test the helper directly with a reader-less, pre-filled channel
// rather than wedging the live streammux: substituting an
// internal-channel field would race with the FromKernel reader
// goroutine, and the test owns the chan here.
func TestBoundedSendInputUnion_ErrPipelineBusyOnFullChan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	full := make(chan packetorframe.InputUnion, 1)
	full <- packetorframe.InputUnion{} // saturate; nothing reads.

	const timeout = 50 * time.Millisecond
	cleanups := 0
	t0 := time.Now()
	err := boundedSendInputUnion(
		ctx,
		full,
		packetorframe.InputUnion{},
		timeout,
		func() { cleanups++ },
	)
	elapsed := time.Since(t0)

	require.Truef(t, errors.Is(err, ErrPipelineBusy),
		"expected ErrPipelineBusy, got %v", err)
	require.GreaterOrEqualf(t, elapsed, timeout,
		"helper returned before timeout (elapsed=%v < %v) — timer fired prematurely",
		elapsed, timeout)
	require.Lessf(t, elapsed, 10*timeout,
		"helper took far longer than the requested timeout (elapsed=%v) — bounded-send budget not honored",
		elapsed)
	require.Equalf(t, 1, cleanups,
		"abortCleanup must run exactly once on the timeout path, ran %d times", cleanups)
}

// TestBoundedSendInputUnion_HappyPath validates the success path:
// when the channel has room, the send completes, returns nil, and
// abortCleanup is NOT called (the consumer now owns the resources).
func TestBoundedSendInputUnion_HappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := make(chan packetorframe.InputUnion, 1)
	cleanups := 0
	err := boundedSendInputUnion(
		ctx,
		ch,
		packetorframe.InputUnion{},
		50*time.Millisecond,
		func() { cleanups++ },
	)
	require.NoError(t, err)
	require.Equalf(t, 0, cleanups,
		"abortCleanup must NOT run when send succeeds, ran %d times", cleanups)
	require.Len(t, ch, 1, "item must be queued in the channel after success")
}

// TestBoundedSendInputUnion_CtxCanceled validates that an
// already-cancelled context short-circuits the send and runs cleanup
// exactly once.
func TestBoundedSendInputUnion_CtxCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	full := make(chan packetorframe.InputUnion, 1)
	full <- packetorframe.InputUnion{}

	cleanups := 0
	err := boundedSendInputUnion(
		ctx,
		full,
		packetorframe.InputUnion{},
		1*time.Second,
		func() { cleanups++ },
	)
	require.ErrorIs(t, err, context.Canceled,
		"cancelled ctx must surface as context.Canceled, not ErrPipelineBusy")
	require.False(t, errors.Is(err, ErrPipelineBusy),
		"ctx cancel path must NOT mask itself as ErrPipelineBusy")
	require.Equalf(t, 1, cleanups,
		"abortCleanup must run exactly once on the ctx-canceled path, ran %d times", cleanups)
}
