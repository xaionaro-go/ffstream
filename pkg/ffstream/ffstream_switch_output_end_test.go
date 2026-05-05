package ffstream

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
)

func TestEndDoesNotWaitForWedgedSwitchOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	err = s.Start(ctx, streammuxtypes.TranscoderConfig{}, streammuxtypes.MuxModeForbid, nil)
	require.NoError(t, err)

	s.StreamMux.Locker.ManualLock(ctx)
	defer s.StreamMux.Locker.ManualUnlock(ctx)

	switchStarted := make(chan struct{})
	switchDone := make(chan error, 1)
	go func() {
		switchDone <- blockedSwitchOutputByProps(ctx, s, switchStarted, streammuxtypes.SenderProps{
			TranscoderConfig: streammuxtypes.TranscoderConfig{},
		})
	}()

	<-switchStarted
	waitForSwitchOutputBlocked(t)

	endDone := make(chan error, 1)
	go func() {
		endDone <- s.End(ctx)
	}()

	select {
	case err := <-endDone:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("End blocked behind a wedged SwitchOutputByProps call")
	}
}

//go:noinline
func blockedSwitchOutputByProps(
	ctx context.Context,
	s *FFStream,
	started chan<- struct{},
	props streammuxtypes.SenderProps,
) error {
	close(started)
	return s.SwitchOutputByProps(ctx, props)
}

func waitForSwitchOutputBlocked(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for {
		stacks := allGoroutineStacks()
		if goroutineStackContainsAll(
			stacks,
			"blockedSwitchOutputByProps",
			"sync.(*RWMutex).Lock",
		) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("switch goroutine did not block inside SwitchOutputByProps:\n%s", stacks)
		}
		runtime.Gosched()
	}
}

func goroutineStackContainsAll(stacks string, needles ...string) bool {
	for _, stack := range strings.Split(stacks, "\n\ngoroutine ") {
		matchesAll := true
		for _, needle := range needles {
			if !strings.Contains(stack, needle) {
				matchesAll = false
				break
			}
		}
		if matchesAll {
			return true
		}
	}
	return false
}

func allGoroutineStacks() string {
	for size := 1 << 20; ; size *= 2 {
		buf := make([]byte, size)
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
	}
}
