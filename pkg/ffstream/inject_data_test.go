package ffstream

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
)

func TestInjectData(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	s.StreamMux, err = streammux.NewWithCustomData[CustomData](ctx, streammuxtypes.MuxModeSameOutputSameTracks, nil)
	require.NoError(t, err)

	err = s.InjectData(ctx, []byte{0x01, 0x02, 0x03}, time.Second)
	require.NoError(t, err)
}
