// ffstream_set_output_url_test.go covers FFStream.SetOutputURL: the
// happy path of replacing a single output template's URL, and the
// guard that requires exactly one OutputTemplate.

package ffstream

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
