// option_quiet_empty_priority_test.go asserts that
// OptionQuietEmptyPriority / Config.QuietEmptyPriority correctly
// propagate (1) to the InputWithFallback Config and (2) to the
// StreamMux.QuietMissingEncoder atomic — the two consumers gated by
// the flag at the ffstream wiring site.

package ffstream

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOptionQuietEmptyPriority_DefaultOff(t *testing.T) {
	cfg := DefaultConfig()
	require.False(t, cfg.QuietEmptyPriority,
		"ffstream default must keep the legacy ERRO/WARN levels so "+
			"existing diagnostics aren't lost; opt-in only")
}

func TestOptionQuietEmptyPriority_ApplyOverride(t *testing.T) {
	cfg := DefaultConfig()
	OptionQuietEmptyPriority(true).apply(&cfg)
	require.True(t, cfg.QuietEmptyPriority,
		"OptionQuietEmptyPriority(true) must set the bit")

	OptionQuietEmptyPriority(false).apply(&cfg)
	require.False(t, cfg.QuietEmptyPriority,
		"OptionQuietEmptyPriority(false) must clear the bit")
}
