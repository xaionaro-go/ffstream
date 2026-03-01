package goconv

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
)

func TestLatenciesRoundTrip(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		assert.Nil(t, LatenciesToGRPC(nil))
	})

	t.Run("full struct round-trip", func(t *testing.T) {
		original := &streammuxtypes.Latencies{
			Audio: streammuxtypes.TrackLatencies{
				PreTranscoding:    10 * time.Millisecond,
				Transcoding:       20 * time.Millisecond,
				TranscodedPreSend: 5 * time.Millisecond,
				Sending:           15 * time.Millisecond,
			},
			Video: streammuxtypes.TrackLatencies{
				PreTranscoding:    30 * time.Millisecond,
				Transcoding:       50 * time.Millisecond,
				TranscodedPreSend: 10 * time.Millisecond,
				Sending:           25 * time.Millisecond,
			},
		}

		grpc := LatenciesToGRPC(original)
		require.NotNil(t, grpc)

		result := LatenciesFromGRPC(grpc)
		require.NotNil(t, result)

		assert.Equal(t, original.Audio.PreTranscoding, result.Audio.PreTranscoding)
		assert.Equal(t, original.Audio.Transcoding, result.Audio.Transcoding)
		assert.Equal(t, original.Audio.TranscodedPreSend, result.Audio.TranscodedPreSend)
		assert.Equal(t, original.Audio.Sending, result.Audio.Sending)
		assert.Equal(t, original.Video.PreTranscoding, result.Video.PreTranscoding)
		assert.Equal(t, original.Video.Transcoding, result.Video.Transcoding)
		assert.Equal(t, original.Video.TranscodedPreSend, result.Video.TranscodedPreSend)
		assert.Equal(t, original.Video.Sending, result.Video.Sending)
	})
}
