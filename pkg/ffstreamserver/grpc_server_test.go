// grpc_server_test.go covers the gRPC handler error mapping for
// AddInput / RemoveInput introduced in Iter 2. Strategy B: instantiate
// the real *ffstream.FFStream via ffstream.New and invoke handlers
// directly (no real gRPC dial) — there is no socket, no transport,
// just exercising the error → codes mapping.

package ffstreamserver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestServer(t *testing.T, ctx context.Context) *GRPCServer {
	t.Helper()
	s, err := ffstream.New(ctx)
	require.NoError(t, err)
	return NewGRPCServer(ctx, s)
}

func TestGRPCServer_AddInput_DuplicatePriorityMapsToAlreadyExists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	_, err := srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://a",
		Priority: 0,
	})
	require.NoError(t, err)

	_, err = srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://b",
		Priority: 0,
	})
	require.Error(t, err)
	require.Equal(t, codes.AlreadyExists, status.Code(err),
		"duplicate AddInput must map to codes.AlreadyExists, got %v: %v", status.Code(err), err)
}

func TestGRPCServer_RemoveInput_NotFoundMapsToNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	_, err := srv.RemoveInput(ctx, &ffstream_grpc.RemoveInputRequest{
		Priority: 0,
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err),
		"RemoveInput on empty FFStream must map to codes.NotFound, got %v: %v", status.Code(err), err)
}

// TestGRPCServer_AddInput_UnknownErrorMapsToUnknown is currently a TODO:
// FFStream.AddInput's only non-sentinel error path is the internal-state
// invariant "len(InputChains) != len(InputsInfo)", which is unreachable
// from a fresh FFStream constructed via ffstream.New. Inducing that
// failure would require either (a) reaching into unexported state or
// (b) mocking *ffstream.FFStream behind an interface — out of scope
// for this iteration's tests-only ECI step. The default switch arm in
// grpc_server.go is straight-line code (status.Errorf with codes.Unknown
// for any non-sentinel error from AddInput) and is covered by visual
// inspection.
func TestGRPCServer_AddInput_UnknownErrorMapsToUnknown(t *testing.T) {
	t.Skip("TODO: requires injectable FFStream interface to surface a non-sentinel error; default arm is trivial straight-line code")
}
