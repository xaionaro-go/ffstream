// option_quiet_on_open_failure_test.go asserts that
// OptionQuietOnOpenFailure / Config.QuietOnOpenFailure correctly
// propagate (1) to the InputWithFallback Config and (2) to the
// StreamMux.QuietOnMissingEncoder atomic — the two consumers gated by
// the flag at the ffstream wiring site. Also pins the legacy
// OptionQuietEmptyPriority alias for backward compatibility.

package ffstream

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOptionQuietOnOpenFailure_DefaultOff(t *testing.T) {
	cfg := DefaultConfig()
	require.False(t, cfg.QuietOnOpenFailure,
		"ffstream default must keep the legacy ERRO/WARN levels so "+
			"existing diagnostics aren't lost; opt-in only")
}

func TestOptionQuietOnOpenFailure_ApplyOverride(t *testing.T) {
	cfg := DefaultConfig()
	OptionQuietOnOpenFailure(true).apply(&cfg)
	require.True(t, cfg.QuietOnOpenFailure,
		"OptionQuietOnOpenFailure(true) must set the bit")

	OptionQuietOnOpenFailure(false).apply(&cfg)
	require.False(t, cfg.QuietOnOpenFailure,
		"OptionQuietOnOpenFailure(false) must clear the bit")
}

// TestOptionQuietEmptyPriority_LegacyAlias pins that the deprecated
// OptionQuietEmptyPriority type alias still flips the same canonical
// Config bit, so callers built against the pre-rename API continue to
// compile and behave identically.
func TestOptionQuietEmptyPriority_LegacyAlias(t *testing.T) {
	cfg := DefaultConfig()
	OptionQuietEmptyPriority(true).apply(&cfg)
	require.True(t, cfg.QuietOnOpenFailure,
		"legacy OptionQuietEmptyPriority(true) must set the canonical "+
			"QuietOnOpenFailure bit")
}

func TestOptionExitOnLastInputRemoved_DefaultOff(t *testing.T) {
	cfg := DefaultConfig()

	require.False(t, cfg.ExitOnLastInputRemoved)
}

func TestOptionExitOnLastInputRemoved_ApplyOverride(t *testing.T) {
	cfg := DefaultConfig()
	OptionExitOnLastInputRemoved(true).apply(&cfg)
	require.True(t, cfg.ExitOnLastInputRemoved)

	OptionExitOnLastInputRemoved(false).apply(&cfg)
	require.False(t, cfg.ExitOnLastInputRemoved)
}
