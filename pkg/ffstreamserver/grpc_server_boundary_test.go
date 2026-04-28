// grpc_server_boundary_test.go verifies that boundary-condition checks in
// GRPCServer methods return structured errors rather than panicking on
// index-out-of-range or nil-dereference situations.

package ffstreamserver

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/kernel"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/observability"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newGRPCServerForBoundaryTest(
	t *testing.T,
	ctx context.Context,
) *GRPCServer {
	t.Helper()
	s, err := ffstream.New(ctx)
	require.NoError(t, err)
	return NewGRPCServer(ctx, s)
}

// TestGRPCServer_SetInputCustomOption_PriorityEqualLen_ReturnsInvalidArgument
// exercises the boundary where req.InputPriority equals len(InputChains).
// Before the fix (> instead of >=), the check passed and the next line indexed
// InputChains[len(InputChains)], panicking with index out of range.
func TestGRPCServer_SetInputCustomOption_PriorityEqualLen_ReturnsInvalidArgument(t *testing.T) {
	ctx := context.Background()
	srv := newGRPCServerForBoundaryTest(t, ctx)

	// Fresh FFStream: InputChains length is 0, so priority==0 hits the boundary.
	require.Equal(t, 0, len(srv.FFStream.Inputs.InputChains),
		"precondition: fresh FFStream must have 0 InputChains")

	var resp *ffstream_grpc.SetInputCustomOptionReply
	var err error
	require.NotPanics(t, func() {
		resp, err = srv.SetInputCustomOption(ctx, &ffstream_grpc.SetInputCustomOptionRequest{
			InputPriority: 0,
			InputNum:      0,
			Key:           "f",
			Value:         "mpegts",
		})
	}, "SetInputCustomOption must not panic when InputPriority == len(InputChains)")

	require.Error(t, err, "SetInputCustomOption must return error on out-of-range priority")
	require.Nil(t, resp, "response must be nil on error")

	st, ok := status.FromError(err)
	require.True(t, ok, "returned error must be a gRPC status")
	assert.Equal(t, codes.InvalidArgument, st.Code(),
		"boundary violation must be reported as InvalidArgument")
}

// TestGRPCServer_SetInputCustomOption_PriorityBelowLen_DoesNotReturnOutOfRange
// is the dual-sided check: when InputPriority is strictly below len(InputChains),
// the boundary check must NOT reject with InvalidArgument/out-of-range.
// This proves the boundary moved in the correct direction (>= vs. >, not
// e.g. flipped to < which would reject every valid index).
func TestGRPCServer_SetInputCustomOption_PriorityBelowLen_DoesNotReturnOutOfRange(t *testing.T) {
	ctx := context.Background()
	srv := newGRPCServerForBoundaryTest(t, ctx)

	// Seed one input so InputChains has length 1.
	_, err := srv.FFStream.AddInput(ctx, ffstream.Resource{
		URL: "file:/does-not-exist",
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(srv.FFStream.Inputs.InputChains),
		"precondition: one InputChain after AddInput")

	// priority 0 (< len 1) must pass the boundary check. It may still fail
	// downstream (e.g. InputNum out of range), but the status must NOT be
	// the out-of-range-priority InvalidArgument we would have emitted at the
	// boundary.
	resp, callErr := srv.SetInputCustomOption(ctx, &ffstream_grpc.SetInputCustomOptionRequest{
		InputPriority: 0,
		InputNum:      0,
		Key:           "f",
		Value:         "mpegts",
	})

	if callErr == nil {
		require.NotNil(t, resp, "on success, reply must be non-nil")
		return
	}
	// If an error was returned, it must not be the out-of-range-priority one.
	st, ok := status.FromError(callErr)
	require.True(t, ok, "returned error must be a gRPC status")
	assert.NotContains(t, st.Message(), "input priority 0 is out of range",
		"valid priority must not trigger the priority out-of-range error")
}

// TestGRPCServer_SetStopInput_PriorityEqualLen_ReturnsInvalidArgument
// exercises the same boundary at the SetStopInput call site.
func TestGRPCServer_SetStopInput_PriorityEqualLen_ReturnsInvalidArgument(t *testing.T) {
	ctx := context.Background()
	srv := newGRPCServerForBoundaryTest(t, ctx)

	require.Equal(t, 0, len(srv.FFStream.Inputs.InputChains),
		"precondition: fresh FFStream must have 0 InputChains")

	var resp *ffstream_grpc.SetStopInputReply
	var err error
	require.NotPanics(t, func() {
		resp, err = srv.SetStopInput(ctx, &ffstream_grpc.SetStopInputRequest{
			InputPriority: 0,
			Stop:          true,
		})
	}, "SetStopInput must not panic when InputPriority == len(InputChains)")

	require.Error(t, err, "SetStopInput must return error on out-of-range priority")
	require.Nil(t, resp, "response must be nil on error")

	st, ok := status.FromError(err)
	require.True(t, ok, "returned error must be a gRPC status")
	assert.Equal(t, codes.InvalidArgument, st.Code(),
		"boundary violation must be reported as InvalidArgument")
}

// TestGRPCServer_SetStopInput_PriorityBelowLen_DoesNotReturnOutOfRange
// is the dual-sided check for SetStopInput: valid priority must not trip
// the boundary rejection.
func TestGRPCServer_SetStopInput_PriorityBelowLen_DoesNotReturnOutOfRange(t *testing.T) {
	ctx := context.Background()
	srv := newGRPCServerForBoundaryTest(t, ctx)

	_, err := srv.FFStream.AddInput(ctx, ffstream.Resource{
		URL: "file:/does-not-exist",
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(srv.FFStream.Inputs.InputChains),
		"precondition: one InputChain after AddInput")

	resp, callErr := srv.SetStopInput(ctx, &ffstream_grpc.SetStopInputRequest{
		InputPriority: 0,
		Stop:          true,
	})

	if callErr == nil {
		require.NotNil(t, resp, "on success, reply must be non-nil")
		return
	}
	st, ok := status.FromError(callErr)
	require.True(t, ok, "returned error must be a gRPC status")
	assert.NotContains(t, st.Message(), "input priority 0 is out of range",
		"valid priority must not trigger the priority out-of-range error")
}

// TestGRPCServer_GetInputsInfo_NilRetryableKernel_DoesNotPanic exercises the
// closure in GetInputsInfo for the case where the retryable kernel has not
// yet been opened: k.Kernel is the zero value (nil *Input). The closure then
// returns nil, and BEFORE the fix, the subsequent inputKernel.GetObjectID()
// would dereference a nil pointer. With the `if inputKernel == nil { continue }`
// guard, the iteration must simply skip this resource.
func TestGRPCServer_GetInputsInfo_NilRetryableKernel_DoesNotPanic(t *testing.T) {
	ctx := context.Background()
	srv := newGRPCServerForBoundaryTest(t, ctx)

	// AddInput populates InputsInfo[0] and creates an InputChain whose
	// retryable kernel is NOT opened (StartOnInit=false).
	_, err := srv.FFStream.AddInput(ctx, ffstream.Resource{
		URL: "file:/does-not-exist",
	})
	require.NoError(t, err)

	var resp *ffstream_grpc.GetInputsInfoReply
	var callErr error
	require.NotPanics(t, func() {
		resp, callErr = srv.GetInputsInfo(ctx, &ffstream_grpc.GetInputsInfoRequest{})
	}, "GetInputsInfo must not panic when the retryable kernel is unopened")

	require.NoError(t, callErr)
	require.NotNil(t, resp)
	// With a nil kernel, the resource is skipped entirely.
	assert.Empty(t, resp.Inputs,
		"resource with unopened kernel must be skipped (no InputInfo emitted)")
}

// TestGRPCServer_GetInputsInfo_Kernel0LenEqualsIdx_DoesNotPanic exercises the
// off-by-one at `if len(k.Kernel.Kernel0) <= idx`. We construct the kernel
// manually so that k.Kernel != nil (passes the prior nil check) but
// len(Kernel0) == idx (which must return nil). Before the fix (`< idx`), the
// check returned false for the equal case and the next line indexed
// Kernel0[idx] out of range.
func TestGRPCServer_GetInputsInfo_Kernel0LenEqualsIdx_DoesNotPanic(t *testing.T) {
	ctx := context.Background()
	srv := newGRPCServerForBoundaryTest(t, ctx)

	// Register two resources at priority 0 so the GetInputsInfo loop runs
	// with idx=0,1.
	for range 2 {
		_, err := srv.FFStream.AddInput(ctx, ffstream.Resource{
			URL: "file:/does-not-exist",
		})
		require.NoError(t, err)
	}
	require.Equal(t, 1, len(srv.FFStream.Inputs.InputChains))

	// Reach into the retryable kernel and simulate an opened kernel whose
	// Tee (Kernel0) has LEN < number of resources. At idx==len(Kernel0) the
	// boundary check must short-circuit.
	//
	// Writes to Kernel/KernelIsSet are wrapped in quiesceRetryable +
	// KernelLocker.Do so the race detector accepts them. GetInputsInfo
	// reads KernelIsSet under KernelLocker (race fix), so the test must
	// write under the same lock; quiesceRetryable terminates the pipeline
	// init goroutine that would otherwise hold the lock indefinitely.
	chain := srv.FFStream.Inputs.InputChains[0]
	retryable := chain.Input.Processor.Kernel
	quiesceRetryable(ctx, t, retryable)
	retryable.KernelLocker.Do(ctx, func() {
		// An empty Tee: len(Kernel0) == 0, so idx==0 and idx==1 both hit
		// the boundary.
		retryable.Kernel = &ffstream.Input{
			Kernel0: kernel.Tee[kernel.Abstract]{},
			Kernel1: nil,
		}
		retryable.KernelIsSet = true
	})

	var resp *ffstream_grpc.GetInputsInfoReply
	var callErr error
	require.NotPanics(t, func() {
		resp, callErr = srv.GetInputsInfo(ctx, &ffstream_grpc.GetInputsInfoRequest{})
	}, "GetInputsInfo must not panic when len(Kernel0) == idx")

	require.NoError(t, callErr)
	require.NotNil(t, resp)
	// Both resources are skipped because len(Kernel0)==0 is <= every idx.
	assert.Empty(t, resp.Inputs,
		"resources with idx >= len(Kernel0) must be skipped")
}

// TestGRPCServer_GetInputsInfo_Kernel0Populated_EmitsInputInfo is the
// dual-sided positive check: when len(Kernel0) > idx AND the item is a
// *kernel.Input, GetInputsInfo must emit an InputInfo. This proves the
// boundary fix did not over-reject valid cases.
func TestGRPCServer_GetInputsInfo_Kernel0Populated_EmitsInputInfo(t *testing.T) {
	ctx := context.Background()
	srv := newGRPCServerForBoundaryTest(t, ctx)

	_, err := srv.FFStream.AddInput(ctx, ffstream.Resource{
		URL: "file:/does-not-exist",
	})
	require.NoError(t, err)
	require.Equal(t, 1, len(srv.FFStream.Inputs.InputChains))

	// Writes to Kernel/KernelIsSet are wrapped in quiesceRetryable +
	// KernelLocker.Do so the race detector accepts them. GetInputsInfo
	// reads KernelIsSet under KernelLocker (race fix), so the test must
	// write under the same lock; quiesceRetryable terminates the pipeline
	// init goroutine that would otherwise hold the lock indefinitely.
	chain := srv.FFStream.Inputs.InputChains[0]
	retryable := chain.Input.Processor.Kernel
	quiesceRetryable(ctx, t, retryable)
	retryable.KernelLocker.Do(ctx, func() {
		// Populate Kernel0 with a single *kernel.Input so idx==0 resolves
		// to it.
		retryable.Kernel = &ffstream.Input{
			Kernel0: kernel.Tee[kernel.Abstract]{&kernel.Input{}},
			Kernel1: nil,
		}
		retryable.KernelIsSet = true
	})

	resp, callErr := srv.GetInputsInfo(ctx, &ffstream_grpc.GetInputsInfoRequest{})
	require.NoError(t, callErr)
	require.NotNil(t, resp)
	require.Len(t, resp.Inputs, 1,
		"one populated, well-typed Kernel0 entry must yield one InputInfo")
	assert.Equal(t, uint64(0), resp.Inputs[0].Priority)
	assert.Equal(t, uint64(0), resp.Inputs[0].Num)
	assert.Equal(t, "file:/does-not-exist", resp.Inputs[0].Url)
}

// TestGRPCServer_GetInputsInfo_ConcurrentWithAddInput launches concurrent
// AddInput writers and GetInputsInfo readers alongside SetSuppressed and
// SetInputCustomOption mutators, and verifies that the readers never panic
// and that cross-field invariants expressed in the canary URL are preserved.
//
// The bug it guards against is that the original GetInputsInfo read
// InputsInfo entries (URL, InputConfig.CustomOptions, Suppressed) without
// holding FFStream.locker, so it raced with AddInput which mutated the
// slice under that lock. The fix snapshots InputsInfo under FFStream.locker
// BEFORE acquiring InputChainsLocker (preserving the AddInput lock order:
// FFStream.locker -> InputChainsLocker), so the snapshot is a race-free
// deep copy.
//
// With -race, this test deterministically flags the bug on a torn read.
// Without -race, it still acts as a smoke test: it verifies absence of
// panic, deadlock, and divergence of the len(InputsInfo)==len(InputChains)
// invariant after the mixed-workload phase.
func TestGRPCServer_GetInputsInfo_ConcurrentWithAddInput(t *testing.T) {
	ctx := context.Background()
	srv := newGRPCServerForBoundaryTest(t, ctx)

	const (
		writers     = 8
		readers     = 4
		writesPerG  = 50
		maxPriority = 20
	)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	// Writers: each goroutine cycles through priorities and appends
	// resources with URLs encoding (priority, resource-index, "ok"). It
	// also calls SetSuppressed and SetInputCustomOption so the reader
	// observes mutations to every race-prone field.
	for w := 0; w < writers; w++ {
		w := w
		wg.Add(1)
		observability.Go(ctx, func(ctx context.Context) {
			defer wg.Done()
			for i := 0; i < writesPerG; i++ {
				priority := uint((w*7 + i) % maxPriority)
				url := fmt.Sprintf("p%d-r?-ok", priority)
				res := ffstream.Resource{
					URL:      url,
					Priority: priority,
					InputConfig: kernel.InputConfig{
						CustomOptions: avptypes.DictionaryItems{
							{Key: "f", Value: "mpegts"},
						},
					},
				}
				if _, err := srv.FFStream.AddInput(ctx, res); err != nil {
					// Out-of-channel-capacity failures are expected at
					// the tail; they must not panic, and the invariant
					// must still hold, which the post-loop check
					// verifies. Continue to exercise the other paths.
					continue
				}
				// Mutate Suppressed and CustomOptions on a recently
				// added resource to exercise SetSuppressed /
				// SetInputCustomOption against the reader.
				_ = srv.FFStream.SetSuppressed(ctx, priority, 0, i%2 == 0)
				_ = srv.FFStream.SetInputCustomOption(
					ctx, priority, 0, "f", "mpegts")
			}
		})
	}

	// Readers: each goroutine calls GetInputsInfo and validates each
	// returned InputInfo. The URL must match the canary pattern for the
	// priority reported in the same record.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		observability.Go(ctx, func(ctx context.Context) {
			defer wg.Done()
			for i := 0; i < writesPerG*2; i++ {
				resp, err := srv.GetInputsInfo(ctx, &ffstream_grpc.GetInputsInfoRequest{})
				if err != nil {
					// GetInputsInfo must never fail in this test.
					t.Errorf("GetInputsInfo returned unexpected error: %v", err)
					return
				}
				require.NotNil(t, resp)
				for _, info := range resp.Inputs {
					// The URL must start with "p<priority>-" and end
					// with "-ok". Any other shape proves a torn read
					// or a mis-indexed resource.
					wantPrefix := fmt.Sprintf("p%d-", info.Priority)
					if len(info.Url) < len(wantPrefix) || info.Url[:len(wantPrefix)] != wantPrefix {
						t.Errorf("torn URL read: Priority=%d URL=%q (expected prefix %q)",
							info.Priority, info.Url, wantPrefix)
						return
					}
					if info.Url[len(info.Url)-3:] != "-ok" {
						t.Errorf("torn URL read: missing \"-ok\" suffix: URL=%q", info.Url)
						return
					}
					// The typed Priority field replaces the old
					// fallback_priority CustomOption canary; the URL
					// prefix check above already ties Priority to the
					// resource that wrote it.
				}
			}
		})
	}

	wg.Wait()

	// Post-run invariant: len(InputsInfo) == len(InputChains).
	require.Equal(t,
		srv.FFStream.Inputs.GetInputChainsCount(ctx),
		len(srv.FFStream.SnapshotInputsInfo(ctx)),
		"invariant len(InputsInfo)==len(InputChains) must hold after concurrent AddInput/GetInputsInfo")
}

// TestFFStream_SnapshotInputsInfo_IsDeepCopy proves that the snapshot
// returned by SnapshotInputsInfo does not alias the live InputsInfo: mutating
// the snapshot's Resources, their CustomOptions entries, or its outer slice
// does not affect subsequent snapshots, and mutating the live InputsInfo
// after the snapshot does not affect the snapshot. This is the invariant
// the NEW-BUG-1 fix relies on: concurrent SetSuppressed /
// SetInputCustomOption cannot perturb a snapshot that a reader is iterating.
func TestFFStream_SnapshotInputsInfo_IsDeepCopy(t *testing.T) {
	ctx := context.Background()
	s, err := ffstream.New(ctx)
	require.NoError(t, err)

	_, err = s.AddInput(ctx, ffstream.Resource{
		URL: "url-0",
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "k", Value: "live"},
			},
		},
	})
	require.NoError(t, err)

	snap1 := s.SnapshotInputsInfo(ctx)
	require.Len(t, snap1, 1)
	require.Len(t, snap1[0], 1)
	assert.Equal(t, "url-0", snap1[0][0].URL)
	require.Len(t, snap1[0][0].CustomOptions, 1)
	assert.Equal(t, "live", snap1[0][0].CustomOptions[0].Value)

	// Mutate the live InputsInfo via the public API.
	err = s.SetInputCustomOption(ctx, 0, 0, "k", "mutated")
	require.NoError(t, err)
	err = s.SetSuppressed(ctx, 0, 0, true)
	require.NoError(t, err)

	// snap1 must remain unchanged — it's a deep copy.
	assert.Equal(t, "live", snap1[0][0].CustomOptions[0].Value,
		"snapshot's CustomOptions must not observe post-snapshot mutations")
	assert.False(t, snap1[0][0].Suppressed,
		"snapshot's Suppressed must not observe post-snapshot mutations")

	// snap2 must observe the mutations.
	snap2 := s.SnapshotInputsInfo(ctx)
	require.Len(t, snap2, 1)
	require.Len(t, snap2[0], 1)
	assert.Equal(t, "mutated", snap2[0][0].CustomOptions[0].Value,
		"second snapshot must observe mutation made between the two snapshots")
	assert.True(t, snap2[0][0].Suppressed,
		"second snapshot must observe Suppressed change")

	// Mutating snap2 must not affect snap1 or the live InputsInfo.
	snap2[0][0].CustomOptions[0].Value = "snap2-modified"
	assert.Equal(t, "live", snap1[0][0].CustomOptions[0].Value,
		"mutating snap2 must not affect snap1")
	snap3 := s.SnapshotInputsInfo(ctx)
	assert.Equal(t, "mutated", snap3[0][0].CustomOptions[0].Value,
		"mutating snap2 must not affect the live InputsInfo")
}
