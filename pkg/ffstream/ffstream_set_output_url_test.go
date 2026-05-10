// ffstream_set_output_url_test.go covers FFStream.SetOutputURL: the
// happy path of replacing a single output template's URL, the guard
// that requires exactly one OutputTemplate, and the elimination of
// any sticky `-f`/`-format` muxer override left over from a boot-time
// launch line (regression for the `-f null -` foot-gun).

package ffstream

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

func TestSetOutputURL_HappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	require.NoError(t, s.AddOutputTemplate(ctx, SenderTemplate{
		URLTemplate: "rtmp://old.example.com/app/stream",
	}))

	const newURL = "rtmp://new.example.com/app/stream"
	require.NoError(t, s.SetOutputURL(ctx, newURL))
	require.Equal(t, newURL, s.OutputTemplates[0].URLTemplate,
		"SetOutputURL must replace the URL of the single output template")
}

// TestSetOutputURL_NoOutputs_LazyCreatesTemplate covers the IDLE-start
// contract: when ffstream boots with zero output templates (rc.local
// supervisor model), the first SetOutputURL RPC from wingout doubles
// as the template-creation entry point. Without this lazy-create the
// gRPC layer has no way to bootstrap the output because there is no
// AddOutputTemplate RPC.
func TestSetOutputURL_NoOutputs_LazyCreatesTemplate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	// No AddOutputTemplate call — len(OutputTemplates) == 0.

	const url = "rtmp://example.com/app/stream"
	require.NoError(t, s.SetOutputURL(ctx, url),
		"SetOutputURL on a zero-template ffstream must lazy-create the template")
	require.Len(t, s.OutputTemplates, 1,
		"SetOutputURL on zero-template state must create exactly one template")
	require.Equal(t, url, s.OutputTemplates[0].URLTemplate,
		"the lazy-created template must carry the supplied URL")
}

// TestSetOutputURL_StripsStickyFormat is a regression test for the
// `-f null -` boot-time launch line foot-gun. When the daemon boots
// with no external sink, the launch script appends `-f null -`, which
// the CLI parser stores as DictionaryItem{Key: "f", Value: "null"}
// in the output template's Options (the leading `-` is stripped by
// convertUnknownOptionsToAVPCustomOptions). A subsequent runtime
// SetOutputURL("rtmp://...") used to update only URLTemplate, leaving
// the sticky `-f null` in place, so the muxer stayed `null` and no
// socket was ever opened. SetOutputURL must strip any `-f`/`-format`
// option so the new URL's scheme determines the muxer.
func TestSetOutputURL_StripsStickyFormat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	require.NoError(t, s.AddOutputTemplate(ctx, SenderTemplate{
		URLTemplate: "-",
		Options: []avptypes.DictionaryItem{
			{Key: "f", Value: "null"},
			// "format" is the long form of "-f" in ffmpeg; both must be stripped.
			{Key: "format", Value: "null"},
			// Unrelated options must be preserved.
			{Key: "movflags", Value: "+faststart"},
		},
	}))

	const newURL = "rtmp://example.com/live/stream"
	require.NoError(t, s.SetOutputURL(ctx, newURL))

	require.Equal(t, newURL, s.OutputTemplates[0].URLTemplate,
		"SetOutputURL must replace the URL of the single output template")

	for _, item := range s.OutputTemplates[0].Options {
		require.NotEqual(t, "f", item.Key,
			"SetOutputURL must strip sticky `-f` muxer override (got %#+v)",
			s.OutputTemplates[0].Options)
		require.NotEqual(t, "format", item.Key,
			"SetOutputURL must strip sticky `-format` muxer override (got %#+v)",
			s.OutputTemplates[0].Options)
	}

	// The unrelated option must survive the strip.
	require.Contains(t, s.OutputTemplates[0].Options,
		avptypes.DictionaryItem{Key: "movflags", Value: "+faststart"},
		"SetOutputURL must only strip `-f`/`-format`, not unrelated options")
}

func TestSetOutputURL_MultipleOutputs_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	require.NoError(t, s.AddOutputTemplate(ctx, SenderTemplate{
		URLTemplate: "rtmp://a.example.com/app/stream",
	}))
	require.NoError(t, s.AddOutputTemplate(ctx, SenderTemplate{
		URLTemplate: "rtmp://b.example.com/app/stream",
	}))

	err := s.SetOutputURL(ctx, "rtmp://new.example.com/app/stream")
	require.Error(t, err,
		"SetOutputURL with multiple output templates must return an error")
	// First template URL must be unchanged on error.
	require.Equal(t, "rtmp://a.example.com/app/stream",
		s.OutputTemplates[0].URLTemplate,
		"failing SetOutputURL must not mutate any template")
}

// The four tests below cover the IDLE-START retry-propagation contract
// landed by Task #161 (RC #1). The pre-fix bug:
// SetOutputURL's case 0 lazy-create dropped the boot-time
// flags.RetryOutputTimeoutOnFailure, leaving the runtime
// SenderTemplate with RetryOutputTimeoutOnFailure=0. That made
// senderFactory.NewSender take the no-retry newOutput() path on first
// open failure → fatal SwitchOutputByProps error → no Output ever
// attached to StreamMux → "nowhere to push to a frame.Output" loop.
//
// Broke-the-code-validation (shared header for the four tests):
//   - Reverting the case-0 cfg-default propagation in SetOutputURL
//     → TestSetOutputURL_LazyCreate_PropagatesDefault FAILS
//     (template.RetryOutputTimeoutOnFailure==0, want wantTimeout).
//   - The ZeroDefault test still PASSES on revert (zero default
//     produces zero); both halves of the dual-sided contract are
//     verified by running the pair together.
//   - Reverting the case-1 "apply default only when existing is zero"
//     branch (so the existing value is always overwritten) →
//     TestSetOutputURL_ExistingTemplate_PreservesExplicit FAILS.
//   - Removing case-1 propagation entirely → TestSetOutputURL_Existing
//     Template_ZeroRetry_AppliesDefault FAILS (template stays at 0).
// Pre-fix run captured under ~/tmp/task161-broke-the-code/ for the
// per-test broke-the-code A/B record.

func TestSetOutputURL_LazyCreate_PropagatesDefault(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const wantTimeout = 10 * time.Minute
	s, err := New(ctx, OptionDefaultRetryOutputTimeoutOnFailure(wantTimeout))
	require.NoError(t, err)
	// No AddOutputTemplate call — len(OutputTemplates) == 0 (case 0).

	const url = "rtmp://example.com/app/stream"
	require.NoError(t, s.SetOutputURL(ctx, url),
		"SetOutputURL on zero-template state must lazy-create the template")
	require.Len(t, s.OutputTemplates, 1)
	require.Equal(t, url, s.OutputTemplates[0].URLTemplate)
	require.Equal(t, wantTimeout, s.OutputTemplates[0].RetryOutputTimeoutOnFailure,
		"lazy-created template must inherit Config.DefaultRetryOutputTimeoutOnFailure")
}

func TestSetOutputURL_LazyCreate_ZeroDefaultProducesZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// No DefaultRetryOutputTimeoutOnFailure option — default stays zero.
	s := newTestFFStream(t, ctx)

	const url = "rtmp://example.com/app/stream"
	require.NoError(t, s.SetOutputURL(ctx, url))
	require.Len(t, s.OutputTemplates, 1)
	require.Equal(t, time.Duration(0), s.OutputTemplates[0].RetryOutputTimeoutOnFailure,
		"zero default must produce zero retry on lazy-create (no spurious non-zero leak)")
}

func TestSetOutputURL_ExistingTemplate_PreservesExplicitRetryTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const explicitTimeout = 5 * time.Minute
	const defaultTimeout = 10 * time.Minute
	s, err := New(ctx, OptionDefaultRetryOutputTimeoutOnFailure(defaultTimeout))
	require.NoError(t, err)
	require.NoError(t, s.AddOutputTemplate(ctx, SenderTemplate{
		URLTemplate:                 "rtmp://old.example.com/app/stream",
		RetryOutputTimeoutOnFailure: explicitTimeout,
	}))

	require.NoError(t, s.SetOutputURL(ctx, "rtmp://new.example.com/app/stream"))
	require.Equal(t, explicitTimeout, s.OutputTemplates[0].RetryOutputTimeoutOnFailure,
		"SetOutputURL must preserve explicit RetryOutputTimeoutOnFailure, not overwrite with default")
}

func TestSetOutputURL_ExistingTemplate_ZeroRetry_AppliesDefault(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const defaultTimeout = 10 * time.Minute
	s, err := New(ctx, OptionDefaultRetryOutputTimeoutOnFailure(defaultTimeout))
	require.NoError(t, err)
	require.NoError(t, s.AddOutputTemplate(ctx, SenderTemplate{
		URLTemplate: "rtmp://old.example.com/app/stream",
		// RetryOutputTimeoutOnFailure intentionally zero — older
		// AddOutputTemplate callers that did not wire the boot flag.
	}))

	require.NoError(t, s.SetOutputURL(ctx, "rtmp://new.example.com/app/stream"))
	require.Equal(t, defaultTimeout, s.OutputTemplates[0].RetryOutputTimeoutOnFailure,
		"SetOutputURL on case-1 template with zero retry must inherit Config default")
}
