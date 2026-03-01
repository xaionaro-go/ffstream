package goconv

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDurationRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
	}{
		{"zero", 0},
		{"1ns", time.Nanosecond},
		{"1ms", time.Millisecond},
		{"1s", time.Second},
		{"1h", time.Hour},
		{"negative", -5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grpcVal := DurationToGRPC(tt.d)
			result := DurationFromGRPC(grpcVal)
			assert.Equal(t, tt.d, result)
		})
	}
}
