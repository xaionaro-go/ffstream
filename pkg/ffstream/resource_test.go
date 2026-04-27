package ffstream

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResources_ByFallbackPriority(t *testing.T) {
	makeRes := func(priority uint) Resource {
		return Resource{
			Priority: priority,
		}
	}

	t.Run("empty", func(t *testing.T) {
		var rs Resources
		assert.Nil(t, rs.ByFallbackPriority())
	})

	t.Run("single resource", func(t *testing.T) {
		rs := Resources{makeRes(0)}
		result := rs.ByFallbackPriority()
		require.Len(t, result, 1)
		assert.Len(t, result[0], 1)
	})

	t.Run("two priorities sorted", func(t *testing.T) {
		rs := Resources{makeRes(1), makeRes(0)}
		result := rs.ByFallbackPriority()
		require.Len(t, result, 2)
		// Priority 0 should come first
		assert.Equal(t, uint(0), result[0][0].GetFallbackPriority())
		assert.Equal(t, uint(1), result[1][0].GetFallbackPriority())
	})

	t.Run("duplicate priorities grouped", func(t *testing.T) {
		r1 := makeRes(0)
		r1.URL = "input1"
		r2 := makeRes(0)
		r2.URL = "input2"
		r3 := makeRes(1)
		r3.URL = "input3"
		rs := Resources{r1, r2, r3}
		result := rs.ByFallbackPriority()
		require.Len(t, result, 2)
		assert.Len(t, result[0], 2, "priority 0 should have 2 resources")
		assert.Len(t, result[1], 1, "priority 1 should have 1 resource")
	})
}
