package ffstream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoveInputCancelsWhenLastInputRemovedAndOptionEnabled(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, OptionExitOnLastInputRemoved(true))
	require.NoError(t, err)
	s.InputsInfo = []Resources{{{URL: "android_camera:"}}}

	cancelled := make(chan struct{})
	s.cancelFunc = func() {
		close(cancelled)
	}

	err = s.RemoveInput(ctx, 0, 0)
	require.NoError(t, err)
	require.Empty(t, s.InputsInfo[0])
	requireClosed(t, cancelled)
}

func TestRemoveInputDoesNotCancelWhenOptionDisabled(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx)
	require.NoError(t, err)
	s.InputsInfo = []Resources{{{URL: "android_camera:"}}}

	cancelled := make(chan struct{})
	s.cancelFunc = func() {
		close(cancelled)
	}

	err = s.RemoveInput(ctx, 0, 0)
	require.NoError(t, err)
	require.Empty(t, s.InputsInfo[0])
	requireNotClosed(t, cancelled)
}

func requireClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()

	select {
	case <-ch:
	default:
		t.Fatal("expected channel to be closed")
	}
}

func requireNotClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()

	select {
	case <-ch:
		t.Fatal("expected channel to remain open")
	default:
	}
}
