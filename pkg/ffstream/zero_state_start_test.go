package ffstream

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	streammux "github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
)

func TestStartZeroInputsZeroOutputsSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	err = s.Start(ctx, streammuxtypes.TranscoderConfig{}, streammuxtypes.MuxModeForbid, nil)
	require.NoError(t, err)
	require.NotNil(t, s.StreamMux)
	require.True(t, s.IsRuntimeReady())
	require.Zero(t, s.Inputs.GetInputChainsCount(ctx))
	require.Empty(t, s.OutputTemplates)
}

func TestIsRuntimeReadyRequiresPostStartState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	s.StreamMux, err = streammux.NewWithCustomData[CustomData](
		ctx,
		streammuxtypes.MuxModeForbid,
		nil,
	)
	require.NoError(t, err)

	require.False(t, s.IsRuntimeReady(),
		"runtime readiness must not be true until Start has completed WaitForStart")
}

func TestSetOutputURLCreatesTemplateAfterZeroStateStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	err = s.Start(ctx, streammuxtypes.TranscoderConfig{}, streammuxtypes.MuxModeForbid, nil)
	require.NoError(t, err)

	const outputURL = "rtmp://activated/"
	err = s.SetOutputURL(ctx, outputURL)
	require.NoError(t, err)
	require.Len(t, s.OutputTemplates, 1)
	require.Equal(t, outputURL, s.OutputTemplates[0].URLTemplate)
}
