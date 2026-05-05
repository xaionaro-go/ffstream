package ffstreamserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func TestEndFallsBackToFFStreamRuntimeCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := ffstream.New(ctx)
	require.NoError(t, err)

	err = s.Start(ctx, streammuxtypes.TranscoderConfig{}, streammuxtypes.MuxModeForbid, nil)
	require.NoError(t, err)

	srv := NewGRPCServer(ctx, s)
	_, err = srv.End(ctx, &ffstream_grpc.EndRequest{})
	require.NoError(t, err)
}

func TestEndWritesMarkerWhenUsingStopTranscodingFunc(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	markerPath := filepath.Join(t.TempDir(), "end-marker")
	t.Setenv("FFSTREAM_END_MARKER_FILE", markerPath)

	s, err := ffstream.New(ctx)
	require.NoError(t, err)

	called := false
	srv := NewGRPCServer(ctx, s)
	srv.stopTranscodingFunc = func() {
		called = true
	}

	_, err = srv.End(ctx, &ffstream_grpc.EndRequest{})
	require.NoError(t, err)
	require.True(t, called)

	markerBytes, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	require.NotEmpty(t, markerBytes)
}

func TestEndMarkerWriteFailurePreservesStopTranscodingFunc(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Setenv("FFSTREAM_END_MARKER_FILE", t.TempDir())

	s, err := ffstream.New(ctx)
	require.NoError(t, err)

	called := false
	srv := NewGRPCServer(ctx, s)
	srv.stopTranscodingFunc = func() {
		called = true
	}

	_, err = srv.End(ctx, &ffstream_grpc.EndRequest{})
	require.Error(t, err)
	require.False(t, called)
	require.NotNil(t, srv.stopTranscodingFunc,
		"End must preserve stopTranscodingFunc so a retry can stop the daemon")

	t.Setenv("FFSTREAM_END_MARKER_FILE", "")
	_, err = srv.End(ctx, &ffstream_grpc.EndRequest{})
	require.NoError(t, err)
	require.True(t, called)
	require.Nil(t, srv.stopTranscodingFunc)
}
