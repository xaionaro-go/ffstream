package ffstream

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
)

func TestEndCancelsStreamContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cancelled := make(chan struct{})
	s := &FFStream{
		cancelFunc: func() {
			cancel()
			close(cancelled)
		},
	}

	if err := s.End(ctx); err != nil {
		t.Fatalf("End returned error: %v", err)
	}

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("End did not call the stream cancel function")
	}

	if ctx.Err() == nil {
		t.Fatal("End did not cancel the stream context")
	}

	if s.cancelFunc != nil {
		t.Fatal("End must clear cancelFunc after a successful stop")
	}

	if err := s.End(context.Background()); err == nil {
		t.Fatal("second End call must fail once the stream is no longer running")
	}
}

func TestEndMakesWaitReturnCleanly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	err = s.Start(ctx, streammuxtypes.TranscoderConfig{}, streammuxtypes.MuxModeForbid, nil)
	require.NoError(t, err)

	require.NoError(t, s.End(ctx))
	require.NoError(t, s.Wait(ctx),
		"End-driven daemon shutdown must be a clean Wait result so the supervisor does not treat Deactivate as a crash")
}

func TestWaitReturnsCanceledWithoutEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	s, err := New(ctx)
	require.NoError(t, err)

	err = s.Start(ctx, streammuxtypes.TranscoderConfig{}, streammuxtypes.MuxModeForbid, nil)
	require.NoError(t, err)

	cancel()

	err = s.Wait(context.Background())
	require.Error(t, err,
		"non-End cancellation must stay non-clean so the supervisor restarts the daemon")
	require.ErrorIs(t, err, context.Canceled)
}

func TestEndWritesConfiguredMarkerBeforeCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	markerPath := t.TempDir() + "/end-marker"
	t.Setenv("FFSTREAM_END_MARKER_FILE", markerPath)

	var cancelCalled bool
	s := &FFStream{
		cancelFunc: func() {
			cancelCalled = true
			require.FileExists(t, markerPath,
				"End must write the clean-shutdown marker before cancelling")
			require.NotEmpty(t, readFileForTest(t, markerPath))
			cancel()
		},
	}

	require.NoError(t, s.End(ctx))
	require.True(t, cancelCalled)
}

func TestEndMarkerWriteFailurePreservesShutdownHandle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Setenv("FFSTREAM_END_MARKER_FILE", t.TempDir())

	var cancelCalled bool
	s := &FFStream{
		cancelFunc: func() {
			cancelCalled = true
			cancel()
		},
	}

	err := s.End(ctx)
	require.Error(t, err)
	require.False(t, cancelCalled,
		"End must not claim shutdown when marker classification failed before cancel")
	require.NotNil(t, s.cancelFunc,
		"End must preserve the shutdown handle so a retry can stop the daemon")

	t.Setenv("FFSTREAM_END_MARKER_FILE", "")
	require.NoError(t, s.End(ctx))
	require.True(t, cancelCalled)
	require.Nil(t, s.cancelFunc)
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
