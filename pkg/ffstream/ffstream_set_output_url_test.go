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

func TestSetOutputURL_NoOutputs_ReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	// No AddOutputTemplate call — len(OutputTemplates) == 0.

	err := s.SetOutputURL(ctx, "rtmp://example.com/app/stream")
	require.Error(t, err,
		"SetOutputURL with zero output templates must return an error")
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
