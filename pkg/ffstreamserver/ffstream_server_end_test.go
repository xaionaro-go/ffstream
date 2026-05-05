package ffstreamserver

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestServeContextEndReturnsBeforeShutdownCancelsRPC(t *testing.T) {
	serveCtx, serveCancel := context.WithCancel(context.Background())
	defer serveCancel()

	s, err := ffstream.New(serveCtx)
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	stopTranscoding := func() {
		close(stopEntered)
		serveCancel()
		<-releaseStop
	}

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- NewWithStop(s, stopTranscoding).ServeContext(serveCtx, listener)
	}()

	conn, err := grpc.NewClient(
		listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := ffstream_grpc.NewFFStreamClient(conn)
	callCtx, callCancel := context.WithTimeout(context.Background(), time.Second)
	defer callCancel()

	_, err = client.End(callCtx, &ffstream_grpc.EndRequest{})
	require.NoError(t, err)

	select {
	case <-stopEntered:
	case <-time.After(time.Second):
		t.Fatal("End reply returned, but shutdown was not scheduled")
	}

	close(releaseStop)

	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("ServeContext did not stop after End-triggered shutdown")
	}
}
