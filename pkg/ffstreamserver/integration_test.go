package ffstreamserver

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/client"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"google.golang.org/grpc"
)

func TestIntegrationInjectData(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Setup FFStream
	s, err := ffstream.New(ctx)
	require.NoError(t, err)

	s.StreamMux, err = streammux.NewWithCustomData[ffstream.CustomData](ctx, streammuxtypes.MuxModeSameOutputSameTracks, nil)
	require.NoError(t, err)

	// 2. Setup gRPC Server
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	grpcServer := grpc.NewServer()
	srv := NewGRPCServer(ctx, s)
	ffstream_grpc.RegisterFFStreamServer(grpcServer, srv)

	go func() {
		_ = grpcServer.Serve(lis)
	}()
	defer grpcServer.Stop()

	// 3. Setup Client
	c := client.New(lis.Addr().String())

	// 4. Inject Data
	err = c.InjectData(ctx, []byte{0x01, 0x02, 0x03}, time.Second)
	require.NoError(t, err)
}
