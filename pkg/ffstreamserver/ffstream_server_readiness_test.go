package ffstreamserver

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestServeContextReturnsNotReadyBeforeRuntimeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s, err := ffstream.New(ctx)
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- New(s).ServeContext(ctx, listener)
	}()

	conn, err := grpc.NewClient(
		listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := ffstream_grpc.NewFFStreamClient(conn)
	callCtx, callCancel := context.WithTimeout(ctx, time.Second)
	defer callCancel()

	_, err = client.GetInputsInfo(callCtx, &ffstream_grpc.GetInputsInfoRequest{})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Unavailable, st.Code())
	require.Contains(t, st.Message(), "ffstream runtime is not ready")

	err = s.Start(ctx, streammuxtypes.TranscoderConfig{}, streammuxtypes.MuxModeForbid, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.End(context.Background())
	})

	_, err = client.GetInputsInfo(ctx, &ffstream_grpc.GetInputsInfoRequest{})
	require.NoError(t, err)

	cancel()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("ServeContext did not stop after context cancellation")
	}
}
