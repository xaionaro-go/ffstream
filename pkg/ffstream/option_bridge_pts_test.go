// option_bridge_pts_test.go asserts that
// OptionBridgePTSAcrossChains / Config.BridgePTSAcrossChains correctly
// propagate to the InputWithFallback InputSwitch.Flags so the per-chain
// PTS-offset bridge in avpipeline (default OFF) is enabled at the
// ffstream wiring site.

package ffstream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	barrierstategetter "github.com/xaionaro-go/avpipeline/kernel/barrier/stategetter"
)

func TestOptionBridgePTSAcrossChains_DefaultOn(t *testing.T) {
	cfg := DefaultConfig()
	require.True(t, cfg.BridgePTSAcrossChains,
		"ffstream default must enable cross-chain PTS bridging "+
			"(prod use-case is the cross-clock-domain switch)")
}

func TestOptionBridgePTSAcrossChains_ApplyOverride(t *testing.T) {
	cfg := DefaultConfig()
	OptionBridgePTSAcrossChains(false).apply(&cfg)
	require.False(t, cfg.BridgePTSAcrossChains,
		"OptionBridgePTSAcrossChains(false) must override the default")

	OptionBridgePTSAcrossChains(true).apply(&cfg)
	require.True(t, cfg.BridgePTSAcrossChains,
		"OptionBridgePTSAcrossChains(true) must restore the bit")
}

// TestNew_SetsBridgePTSFlag_ByDefault asserts that ffstream.New, called
// with no options, sets SwitchFlagBridgePTSAcrossChains on the
// InputWithFallback InputSwitch — the load-bearing wiring step that
// makes the avpipeline 500d143 PTS bridge actually fire in production.
func TestNew_SetsBridgePTSFlag_ByDefault(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx)
	require.NoError(t, err)
	require.NotNil(t, s)
	require.NotNil(t, s.Inputs)
	require.NotNil(t, s.Inputs.InputSwitch)

	require.True(t,
		s.Inputs.InputSwitch.Flags.HasAny(barrierstategetter.SwitchFlagBridgePTSAcrossChains),
		"InputSwitch.Flags must carry SwitchFlagBridgePTSAcrossChains by default")
}

// TestNew_BridgePTSFlag_DisabledByOption asserts the operator override
// path: passing OptionBridgePTSAcrossChains(false) clears the bit at
// the InputSwitch construction site.
func TestNew_BridgePTSFlag_DisabledByOption(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, OptionBridgePTSAcrossChains(false))
	require.NoError(t, err)
	require.NotNil(t, s)
	require.NotNil(t, s.Inputs)
	require.NotNil(t, s.Inputs.InputSwitch)

	require.False(t,
		s.Inputs.InputSwitch.Flags.HasAny(barrierstategetter.SwitchFlagBridgePTSAcrossChains),
		"OptionBridgePTSAcrossChains(false) must clear the bit on InputSwitch.Flags")
}
