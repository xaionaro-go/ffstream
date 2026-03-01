package ffstream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/kernel"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

func TestResource_GetFallbackPriority(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		resource Resource
		want     uint
	}{
		{
			name: "priority present",
			resource: Resource{
				InputConfig: kernel.InputConfig{
					CustomOptions: avptypes.DictionaryItems{
						{Key: "fallback_priority", Value: "2"},
					},
				},
			},
			want: 2,
		},
		{
			name:     "no custom options",
			resource: Resource{},
			want:     0,
		},
		{
			name: "explicit zero",
			resource: Resource{
				InputConfig: kernel.InputConfig{
					CustomOptions: avptypes.DictionaryItems{
						{Key: "fallback_priority", Value: "0"},
					},
				},
			},
			want: 0,
		},
		{
			name: "invalid value defaults to 0",
			resource: Resource{
				InputConfig: kernel.InputConfig{
					CustomOptions: avptypes.DictionaryItems{
						{Key: "fallback_priority", Value: "not_a_number"},
					},
				},
			},
			want: 0,
		},
		{
			name: "among other options",
			resource: Resource{
				InputConfig: kernel.InputConfig{
					CustomOptions: avptypes.DictionaryItems{
						{Key: "f", Value: "flv"},
						{Key: "fallback_priority", Value: "5"},
						{Key: "other", Value: "value"},
					},
				},
			},
			want: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.resource.GetFallbackPriority(ctx)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResources_ByFallbackPriority(t *testing.T) {
	ctx := context.Background()

	makeRes := func(priority string) Resource {
		return Resource{
			InputConfig: kernel.InputConfig{
				CustomOptions: avptypes.DictionaryItems{
					{Key: "fallback_priority", Value: priority},
				},
			},
		}
	}

	t.Run("empty", func(t *testing.T) {
		var rs Resources
		assert.Nil(t, rs.ByFallbackPriority(ctx))
	})

	t.Run("single resource", func(t *testing.T) {
		rs := Resources{makeRes("0")}
		result := rs.ByFallbackPriority(ctx)
		require.Len(t, result, 1)
		assert.Len(t, result[0], 1)
	})

	t.Run("two priorities sorted", func(t *testing.T) {
		rs := Resources{makeRes("1"), makeRes("0")}
		result := rs.ByFallbackPriority(ctx)
		require.Len(t, result, 2)
		// Priority 0 should come first
		assert.Equal(t, uint(0), result[0][0].GetFallbackPriority(ctx))
		assert.Equal(t, uint(1), result[1][0].GetFallbackPriority(ctx))
	})

	t.Run("duplicate priorities grouped", func(t *testing.T) {
		r1 := makeRes("0")
		r1.URL = "input1"
		r2 := makeRes("0")
		r2.URL = "input2"
		r3 := makeRes("1")
		r3.URL = "input3"
		rs := Resources{r1, r2, r3}
		result := rs.ByFallbackPriority(ctx)
		require.Len(t, result, 2)
		assert.Len(t, result[0], 2, "priority 0 should have 2 resources")
		assert.Len(t, result[1], 1, "priority 1 should have 1 resource")
	})
}
