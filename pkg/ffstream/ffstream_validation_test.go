//go:build with_libav

package ffstream

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
)

func TestFFStream_SetFPSFraction_Validation(t *testing.T) {
	ctx := context.Background()

	t.Run("nil StreamMux", func(t *testing.T) {
		s := &FFStream{}
		err := s.SetFPSFraction(ctx, 1, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only after Start is invoked")
	})
}

func TestFFStream_SwitchOutputByProps_Validation(t *testing.T) {
	ctx := context.Background()

	t.Run("nil StreamMux", func(t *testing.T) {
		s := &FFStream{}
		err := s.SwitchOutputByProps(ctx, streammuxtypes.SenderProps{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "only after Start is invoked")
	})
}

func TestFFStream_SetSuppressed(t *testing.T) {
	ctx := context.Background()

	s := &FFStream{
		InputsInfo: []Resources{
			{
				{Suppressed: false},
				{Suppressed: false},
			},
		},
	}

	t.Run("valid suppression", func(t *testing.T) {
		err := s.SetSuppressed(ctx, 0, 1, true)
		require.NoError(t, err)
		assert.True(t, s.InputsInfo[0][1].Suppressed)

		err = s.SetSuppressed(ctx, 0, 1, false)
		require.NoError(t, err)
		assert.False(t, s.InputsInfo[0][1].Suppressed)
	})

	t.Run("priority out of range", func(t *testing.T) {
		err := s.SetSuppressed(ctx, 5, 0, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})

	t.Run("num out of range", func(t *testing.T) {
		err := s.SetSuppressed(ctx, 0, 5, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})
}

func TestFFStream_GetBitRates_NilStreamMux(t *testing.T) {
	ctx := context.Background()

	t.Run("nil ffstream", func(t *testing.T) {
		var s *FFStream
		result, err := s.GetBitRates(ctx)
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "ffstream is nil")
	})

	t.Run("nil StreamMux", func(t *testing.T) {
		s := &FFStream{}
		result, err := s.GetBitRates(ctx)
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "only after Start is invoked")
	})
}

func TestFFStream_GetLatencies_NilStreamMux(t *testing.T) {
	ctx := context.Background()

	t.Run("nil ffstream", func(t *testing.T) {
		var s *FFStream
		result, err := s.GetLatencies(ctx)
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "ffstream is nil")
	})

	t.Run("nil StreamMux", func(t *testing.T) {
		s := &FFStream{}
		result, err := s.GetLatencies(ctx)
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "only after Start is invoked")
	})
}

func TestFFStream_Options(t *testing.T) {
	t.Run("default config", func(t *testing.T) {
		cfg := DefaultConfig()
		assert.Equal(t, time.Duration(-1), cfg.InputRetryInterval)
	})

	t.Run("with retry interval", func(t *testing.T) {
		cfg := Options{OptionInputRetryInterval(5 * time.Second)}.Config()
		assert.Equal(t, 5*time.Second, cfg.InputRetryInterval)
	})

	t.Run("empty options use defaults", func(t *testing.T) {
		cfg := Options{}.Config()
		assert.Equal(t, time.Duration(-1), cfg.InputRetryInterval)
	})
}
