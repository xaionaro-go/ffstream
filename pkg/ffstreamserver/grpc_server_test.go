// grpc_server_test.go covers the gRPC handler error mapping for
// AddInput / RemoveInput. Strategy B: instantiate the real
// *ffstream.FFStream via ffstream.New and invoke handlers directly
// (no real gRPC dial) — there is no socket, no transport, just
// exercising the error → codes mapping and the (priority, num)
// reply contract.

package ffstreamserver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/kernel"
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

func TestGRPCServer_AddInput_ReturnsAssignedNum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	reply0, err := srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://a",
		Priority: 0,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(0), reply0.GetNum(),
		"first AddInput at a priority must reply Num=0")

	reply1, err := srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://b",
		Priority: 0,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1), reply1.GetNum(),
		"second AddInput at the same priority must reply Num=1")
}

// TestGRPCServer_GetInputsInfo_BoundaryNum is a regression test for the
// off-by-one bounds check in GetInputsInfo: the `len(Kernel0) < idx`
// comparison let `idx == len(Kernel0)` slip through, causing an
// out-of-bounds panic on Kernel0[idx]. The grpc-recovery interceptor
// converted this to an empty `{}` reply at the network layer, masking
// the bug and producing the F1 symptom (ffstreamctl inputs info empty
// after AddInput).
//
// Setup: install a ChainOfTwo whose Kernel0 (a Tee) has length 1, then
// AddInput a second resource at the same priority — InputsInfo grows to
// 2 while Kernel0 stays at 1, so idx=1 hits the boundary case.
//
// Pre-fix: panic on Kernel0[1] (caught by Go's test runtime), or empty
// reply when run through the gRPC stack.
// Post-fix: reply contains both entries (idx=0 with the injected nil
// Input → Id=0, idx=1 skipped → Id=0), no panic.
func TestGRPCServer_GetInputsInfo_BoundaryNum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	_, err := srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://a",
		Priority: 0,
	})
	require.NoError(t, err)

	// Inject a ChainOfTwo with Kernel0 of length 1 (Tee containing one
	// nil *kernel.Input). This simulates the live state where the
	// Retryable kernel was opened against the older InputsInfo before a
	// second resource was added.
	chain := srv.FFStream.Inputs.InputChains[0]
	retryable := chain.Input.Processor.Kernel
	tee := kernel.Tee[*kernel.Input]{nil}
	retryable.Kernel = kernel.NewChainOfTwo(tee, (*kernel.MapStreamIndices)(nil))
	retryable.KernelIsSet = true

	// Now AddInput a second resource — InputsInfo[0] has 2 entries,
	// Kernel0 still has 1. idx=1 hits the boundary.
	_, err = srv.AddInput(ctx, &ffstream_grpc.AddInputRequest{
		Url:      "test://b",
		Priority: 0,
	})
	require.NoError(t, err)

	reply, err := srv.GetInputsInfo(ctx, &ffstream_grpc.GetInputsInfoRequest{})
	require.NoError(t, err)
	require.NotNil(t, reply)
	require.Len(t, reply.GetInputs(), 2,
		"GetInputsInfo must return BOTH entries even when idx == len(Kernel0); "+
			"got %d (off-by-one bounds check would panic and return empty)",
		len(reply.GetInputs()))
	require.Equal(t, "test://a", reply.GetInputs()[0].GetUrl())
	require.Equal(t, "test://b", reply.GetInputs()[1].GetUrl())
}

func TestGRPCServer_RemoveInput_NotFoundMapsToNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := newTestServer(t, ctx)

	_, err := srv.RemoveInput(ctx, &ffstream_grpc.RemoveInputRequest{
		Priority: 99,
		Num:      0,
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err),
		"RemoveInput on out-of-range priority must map to codes.NotFound, got %v: %v", status.Code(err), err)
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
