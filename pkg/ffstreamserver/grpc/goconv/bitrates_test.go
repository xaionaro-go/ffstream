package goconv

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
)

func TestBitRatesRoundTrip(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		assert.Nil(t, BitRatesToGRPC(nil))
	})

	t.Run("full struct round-trip", func(t *testing.T) {
		original := &streammuxtypes.BitRates{
			Input: streammuxtypes.BitRateInfo{
				Video: 5000000,
				Audio: 128000,
			},
			Encoded: streammuxtypes.BitRateInfo{
				Video: 4500000,
				Audio: 96000,
			},
			Output: streammuxtypes.BitRateInfo{
				Video: 4500000,
				Audio: 96000,
			},
		}

		grpc := BitRatesToGRPC(original)
		require.NotNil(t, grpc)

		result := BitRatesFromGRPC(grpc)
		require.NotNil(t, result)

		assert.Equal(t, original.Input.Video, result.Input.Video)
		assert.Equal(t, original.Input.Audio, result.Input.Audio)
		assert.Equal(t, original.Encoded.Video, result.Encoded.Video)
		assert.Equal(t, original.Output.Audio, result.Output.Audio)
	})
}
