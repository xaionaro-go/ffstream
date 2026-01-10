package ffstream

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
)

func TestInjectSubtitles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	s.StreamMux, err = streammux.NewWithCustomData[CustomData](ctx, streammuxtypes.MuxModeSameOutputSameTracks, nil)
	require.NoError(t, err)

	err = s.InjectSubtitles(ctx, []byte("Hello World"), time.Second)
	require.NoError(t, err)
}
