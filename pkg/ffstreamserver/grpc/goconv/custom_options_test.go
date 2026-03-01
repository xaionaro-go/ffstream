package goconv

import (
	"testing"

	"github.com/stretchr/testify/assert"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
)

func TestCustomOptionsRoundTrip(t *testing.T) {
	t.Run("empty slice", func(t *testing.T) {
		var opts []streammuxtypes.DictionaryItem
		grpc := CustomOptionsToGRPC(opts)
		result := CustomOptionsFromGRPC(grpc)
		assert.Empty(t, result)
	})

	t.Run("multiple options preserved", func(t *testing.T) {
		original := []streammuxtypes.DictionaryItem{
			{Key: "preset", Value: "ultrafast"},
			{Key: "tune", Value: "zerolatency"},
			{Key: "crf", Value: "23"},
		}
		grpc := CustomOptionsToGRPC(original)
		result := CustomOptionsFromGRPC(grpc)
		assert.Equal(t, original, result)
	})
}
