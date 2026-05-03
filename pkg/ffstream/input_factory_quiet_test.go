// input_factory_quiet_test.go pins the propagation of
// FFStream.Config.QuietOnOpenFailure into InputFactory.quietOnOpenFailure
// and from there into kernel.InputConfig.QuietOnOpenFailure on every
// NewInput call. The avpipeline kernel uses that bit to demote
// open-failure log noise (format-from-URL Warn, AsyncOpen Errorf) to
// Debug — see avpipeline/kernel/input.go and the avpipeline
// kernel/input_quiet_test.go suite. The field is package-private; this
// test reads it directly because it lives in the same package.

package ffstream

import (
	"context"
	"sync"
	"testing"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/facebookincubator/go-belt/tool/logger/implementation/logrus"
	loggertypes "github.com/facebookincubator/go-belt/tool/logger/types"
	"github.com/stretchr/testify/require"
)

// quietRecordingHook captures every Entry pushed to the logger so a
// test can assert exactly which Level a given call site emitted at.
type quietRecordingHook struct {
	mu      sync.Mutex
	entries []loggertypes.Entry
}

var _ loggertypes.Hook = (*quietRecordingHook)(nil)

func (h *quietRecordingHook) ProcessLogEntry(e *loggertypes.Entry) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, *e)
	return true
}

func (h *quietRecordingHook) Flush() {}

func (h *quietRecordingHook) snapshot() []loggertypes.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]loggertypes.Entry, len(h.entries))
	copy(out, h.entries)
	return out
}

func ctxWithQuietRecordingHook(t *testing.T) (context.Context, *quietRecordingHook) {
	t.Helper()
	hook := &quietRecordingHook{}
	l := logrus.Default().WithLevel(logger.LevelTrace).WithHooks(hook)
	return logger.CtxWithLogger(context.Background(), l), hook
}

func hasLevel(entries []loggertypes.Entry, lvl logger.Level) bool {
	for _, e := range entries {
		if e.Level == lvl {
			return true
		}
	}
	return false
}

// TestInputFactory_PropagatesQuietOnOpenFailure_True verifies that
// constructing an FFStream with Config.QuietOnOpenFailure=true seeds
// every InputFactory's quietOnOpenFailure to true, and that NewInput
// on a bogus URL emits ZERO WARN/ERROR entries (the behavioral
// confirmation that the bit reaches kernel.InputConfig.QuietOnOpenFailure
// inside avpipeline kernel/input.go).
func TestInputFactory_PropagatesQuietOnOpenFailure_True(t *testing.T) {
	ctx, hook := ctxWithQuietRecordingHook(t)

	s, err := New(ctx, OptionQuietOnOpenFailure(true))
	require.NoError(t, err)

	f := newInputFactory(s, 0)
	require.True(t, f.quietOnOpenFailure,
		"newInputFactory must seed quietOnOpenFailure from Config.QuietOnOpenFailure")

	// Behavioral confirmation: a bogus URL would normally trip site A's
	// "attempting to detect input format from URL" Warn at libav-side
	// open. With the bit propagated all the way into kernel.InputConfig,
	// the Warn must demote to Debug.
	s.InputsInfo = []Resources{
		{
			{URL: "bogusscheme://nonexistent.invalid/q-true"},
		},
	}
	_, _ = f.NewInput(ctx, nil)

	entries := hook.snapshot()
	require.False(t, hasLevel(entries, logger.LevelWarning),
		"QuietOnOpenFailure=true must NOT emit WARN entries; got %v", entries)
	require.False(t, hasLevel(entries, logger.LevelError),
		"QuietOnOpenFailure=true must NOT emit ERROR entries; got %v", entries)
}

// TestInputFactory_PropagatesQuietOnOpenFailure_False verifies that
// constructing an FFStream with Config.QuietOnOpenFailure=false (the
// default) leaves InputFactory.quietOnOpenFailure=false, and that
// NewInput on a bogus URL emits the legacy WARN entry from site A in
// avpipeline kernel/input.go.
func TestInputFactory_PropagatesQuietOnOpenFailure_False(t *testing.T) {
	ctx, hook := ctxWithQuietRecordingHook(t)

	// Default config has QuietOnOpenFailure=false; no option needed.
	s, err := New(ctx)
	require.NoError(t, err)
	require.False(t, s.Config.QuietOnOpenFailure, "sanity: default must be false")

	f := newInputFactory(s, 0)
	require.False(t, f.quietOnOpenFailure,
		"default newInputFactory must have quietOnOpenFailure=false")

	s.InputsInfo = []Resources{
		{
			{URL: "bogusscheme://nonexistent.invalid/q-false"},
		},
	}
	_, _ = f.NewInput(ctx, nil)

	entries := hook.snapshot()
	require.True(t, hasLevel(entries, logger.LevelWarning),
		"default (QuietOnOpenFailure=false) must emit at least one WARN entry; got %v", entries)
}
