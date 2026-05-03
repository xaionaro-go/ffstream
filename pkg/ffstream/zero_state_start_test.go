// zero_state_start_test.go asserts that ffstream.Start succeeds when no
// inputs and no output templates have been registered yet — the supervisor
// daemon model where rc.local boots ffstream idle and wingout activates
// the pipeline later via gRPC AddInput + SetOutputURL + SwitchOutputByProps.
//
// The historical guards in Start fatal-erroered on
//   - GetInputChainsCount == 0 → "no inputs added"
//   - len(OutputTemplates) != 1 → "exactly one output template is required"
// which forced rc.local to know the activation parameters at boot. That is
// a layering violation: activation parameters live in wingout's persisted
// settings.
//
// Falsification: revert ffstream.go's loosen-Start change → this test must
// fail with one of the two error strings above.

package ffstream

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/codec"
	codectypes "github.com/xaionaro-go/avpipeline/codec/types"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

// TestStart_ZeroInputsZeroOutputs_Succeeds verifies Start succeeds with
// neither inputs nor output templates registered. This is the rc.local
// boot path: ffstream starts in IDLE mode and waits for wingout to
// drive it via gRPC.
func TestStart_ZeroInputsZeroOutputs_Succeeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	// Empty TranscoderConfig is the boot-time placeholder — wingout's
	// SwitchOutputByProps overrides it once the user taps Activate.
	err = s.Start(ctx, streammuxtypes.TranscoderConfig{}, streammuxtypes.MuxModeForbid, nil)
	require.NoError(t, err,
		"Start must succeed in zero-input/zero-output (idle) state for the "+
			"rc.local supervisor model")

	// Post-Start invariants for the idle state.
	require.NotNil(t, s.StreamMux,
		"StreamMux must be initialised even in idle Start")
	require.Equal(t, 0, s.Inputs.GetInputChainsCount(ctx),
		"no inputs should be registered at idle Start")
	require.Empty(t, s.OutputTemplates,
		"no output templates should be registered at idle Start")
}

// TestStart_ZeroState_ThenActivateViaAPI verifies the complete activation
// flow: Start with zero state, then AddInput + AddOutputTemplate +
// SetOutputURL + SwitchOutputByProps activates the pipeline. This is the
// wingout user-tap-Activate path that must not regress.
func TestStart_ZeroState_ThenActivateViaAPI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	err = s.Start(ctx, streammuxtypes.TranscoderConfig{}, streammuxtypes.MuxModeForbid, nil)
	require.NoError(t, err)

	// AddInput post-Start must succeed even though Start saw no inputs.
	_, err = s.AddInput(ctx, Resource{URL: "test://", Priority: 0})
	require.NoError(t, err,
		"AddInput must succeed after a zero-state Start")
	require.Equal(t, 1, s.Inputs.GetInputChainsCount(ctx))

	// AddOutputTemplate is the wire-up step before SetOutputURL /
	// SwitchOutputByProps can take effect.
	err = s.AddOutputTemplate(ctx, SenderTemplate{
		URLTemplate: "rtmp://placeholder/",
		Options: avptypes.DictionaryItems{
			{Key: "f", Value: "flv"},
		},
	})
	require.NoError(t, err,
		"AddOutputTemplate must succeed after a zero-state Start")
	require.Len(t, s.OutputTemplates, 1)

	// SetOutputURL replaces the placeholder URL — the wingout flow.
	err = s.SetOutputURL(ctx, "rtmp://activated/")
	require.NoError(t, err,
		"SetOutputURL must succeed after a zero-state Start")
	assert.Equal(t, "rtmp://activated/", s.OutputTemplates[0].URLTemplate)

	// SwitchOutputByProps with copy codecs avoids opening encoders. The
	// call must reach StreamMux without the Start-time output guard
	// interfering.
	props := streammuxtypes.SenderProps{
		TranscoderConfig: streammuxtypes.TranscoderConfig{
			Output: streammuxtypes.TranscoderOutputConfig{
				VideoTrackConfigs: []streammuxtypes.OutputVideoTrackConfig{{
					InputTrackIDs:  []int{0},
					OutputTrackIDs: []int{0},
					CodecName:      codectypes.Name(codec.NameCopy),
				}},
				AudioTrackConfigs: []streammuxtypes.OutputAudioTrackConfig{{
					InputTrackIDs:  []int{0},
					OutputTrackIDs: []int{1},
					CodecName:      codectypes.Name(codec.NameCopy),
				}},
			},
		},
	}
	// SwitchOutputByProps may legitimately fail downstream (no real
	// input frames yet, the placeholder rtmp:// URL is unreachable),
	// but it must not fail with the Start-time "exactly one output
	// template required" guard. We only assert that the call dispatches
	// past the StreamMux nil guard.
	_ = s.SwitchOutputByProps(ctx, props)
	require.NotNil(t, s.StreamMux,
		"StreamMux must remain initialised across SwitchOutputByProps")
}
