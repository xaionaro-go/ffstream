// grpc_server_reinit_encoder_test.go covers the gRPC handler error
// mapping for ReinitEncoder. The Strategy B pattern (see
// grpc_server_test.go) is reused: a real *ffstream.FFStream from
// ffstream.New is wired into a real *GRPCServer, and the handler is
// invoked directly. This exercises the error → status.Code mapping
// without spinning up a transport.
//
// Why no happy-path test here: hitting the success branch requires a
// fully-initialized StreamMux with an active video Output and a
// non-copy/non-raw EncoderFull, which in turn requires a libav encoder
// open. That is the domain of the e2e/integration suites (which
// already exist). The unit test scope is the pre-Start guard and the
// "no encoder" guard — both rely on FailedPrecondition being returned
// for unmet preconditions instead of the bare gRPC default.

package ffstreamserver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestGRPCServer_ReinitEncoder_PreStart_FailedPrecondition asserts that
// invoking ReinitEncoder before Start has been called returns
// FailedPrecondition with a clear message — not the default Unknown
// code, and not a panic. This is the precondition-violation arm:
// FFStream.StreamMux is still nil.
func TestGRPCServer_ReinitEncoder_PreStart_FailedPrecondition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	reply, err := srv.ReinitEncoder(ctx, &ffstream_grpc.ReinitEncoderRequest{})
	require.Error(t, err)
	require.Nil(t, reply)

	st, ok := status.FromError(err)
	require.True(t, ok, "error must be a gRPC status: %v", err)
	require.Equal(t, codes.FailedPrecondition, st.Code(),
		"pre-Start ReinitEncoder must surface FailedPrecondition, got %v: %v",
		st.Code(), st.Message())
	require.Contains(t, st.Message(), "Start",
		"error message must mention Start to guide the operator: %q", st.Message())
}
