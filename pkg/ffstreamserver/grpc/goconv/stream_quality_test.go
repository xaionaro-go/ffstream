package goconv

import (
	"testing"

	"github.com/stretchr/testify/assert"
	quality "github.com/xaionaro-go/avpipeline/packetorframe/filter/quality/types"
)

func TestStreamQualityRoundTrip(t *testing.T) {
	original := quality.StreamQuality{
		Continuity: 0.95,
		Overlap:    0.01,
		FrameRate:  30.0,
		InvalidDTS: 2,
	}

	grpc := StreamQualityToGRPC(original)
	result := StreamQualityFromGRPC(grpc)

	assert.InDelta(t, original.Continuity, result.Continuity, 0.001)
	assert.InDelta(t, original.Overlap, result.Overlap, 0.001)
	assert.InDelta(t, original.FrameRate, result.FrameRate, 0.001)
	assert.Equal(t, original.InvalidDTS, result.InvalidDTS)
}

func TestStreamQualityFromGRPC_NilInput(t *testing.T) {
	result := StreamQualityFromGRPC(nil)
	assert.Equal(t, quality.StreamQuality{}, result)
}
