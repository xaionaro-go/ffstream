package goconv

import (
	"testing"

	"github.com/stretchr/testify/assert"
	avpipeline_grpc "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
)

func TestAddNodeCountersItem(t *testing.T) {
	t.Run("both nil", func(t *testing.T) {
		assert.Nil(t, AddNodeCountersItem(nil, nil))
	})

	t.Run("a nil", func(t *testing.T) {
		b := &avpipeline_grpc.NodeCountersItem{Count: 5, Bytes: 100}
		result := AddNodeCountersItem(nil, b)
		assert.Equal(t, b, result)
	})

	t.Run("b nil", func(t *testing.T) {
		a := &avpipeline_grpc.NodeCountersItem{Count: 3, Bytes: 50}
		result := AddNodeCountersItem(a, nil)
		assert.Equal(t, a, result)
	})

	t.Run("both non-nil", func(t *testing.T) {
		a := &avpipeline_grpc.NodeCountersItem{Count: 3, Bytes: 50}
		b := &avpipeline_grpc.NodeCountersItem{Count: 5, Bytes: 100}
		result := AddNodeCountersItem(a, b)
		assert.Equal(t, uint64(8), result.Count)
		assert.Equal(t, uint64(150), result.Bytes)
	})
}

func TestAddNodeCountersSubSection(t *testing.T) {
	t.Run("both nil", func(t *testing.T) {
		assert.Nil(t, AddNodeCountersSubSection(nil, nil))
	})

	t.Run("a nil returns b", func(t *testing.T) {
		b := &avpipeline_grpc.NodeCountersSubSection{
			Video: &avpipeline_grpc.NodeCountersItem{Count: 10},
		}
		result := AddNodeCountersSubSection(nil, b)
		assert.Equal(t, b, result)
	})

	t.Run("both non-nil sums all fields", func(t *testing.T) {
		a := &avpipeline_grpc.NodeCountersSubSection{
			Video:   &avpipeline_grpc.NodeCountersItem{Count: 10, Bytes: 200},
			Audio:   &avpipeline_grpc.NodeCountersItem{Count: 5, Bytes: 100},
			Unknown: &avpipeline_grpc.NodeCountersItem{Count: 1},
		}
		b := &avpipeline_grpc.NodeCountersSubSection{
			Video: &avpipeline_grpc.NodeCountersItem{Count: 20, Bytes: 400},
			Audio: &avpipeline_grpc.NodeCountersItem{Count: 10, Bytes: 200},
			Other: &avpipeline_grpc.NodeCountersItem{Count: 2},
		}
		result := AddNodeCountersSubSection(a, b)
		assert.Equal(t, uint64(30), result.Video.Count)
		assert.Equal(t, uint64(600), result.Video.Bytes)
		assert.Equal(t, uint64(15), result.Audio.Count)
		assert.Equal(t, uint64(1), result.Unknown.Count)
		assert.Equal(t, uint64(2), result.Other.Count)
	})
}

func TestAddNodeCountersSection(t *testing.T) {
	t.Run("both nil", func(t *testing.T) {
		assert.Nil(t, AddNodeCountersSection(nil, nil))
	})

	t.Run("both non-nil", func(t *testing.T) {
		a := &avpipeline_grpc.NodeCountersSection{
			Packets: &avpipeline_grpc.NodeCountersSubSection{
				Video: &avpipeline_grpc.NodeCountersItem{Count: 100},
			},
			Frames: &avpipeline_grpc.NodeCountersSubSection{
				Audio: &avpipeline_grpc.NodeCountersItem{Count: 50},
			},
		}
		b := &avpipeline_grpc.NodeCountersSection{
			Packets: &avpipeline_grpc.NodeCountersSubSection{
				Video: &avpipeline_grpc.NodeCountersItem{Count: 200},
			},
		}
		result := AddNodeCountersSection(a, b)
		assert.Equal(t, uint64(300), result.Packets.Video.Count)
		assert.Equal(t, uint64(50), result.Frames.Audio.Count)
	})
}
