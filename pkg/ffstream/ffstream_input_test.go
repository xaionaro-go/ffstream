// ffstream_input_test.go covers the Iter 2 input bookkeeping in
// FFStream.AddInput / FFStream.RemoveInput: priority allocation, sentinel
// errors, and the unpause-after-remove cycle. These tests do not exercise
// real I/O — kernel.NewInput is invoked lazily by the input chain when it
// is actually served, which never happens here.

package ffstream

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestFFStream constructs an FFStream the same way TestInjectSubtitles
// does; AddInput / RemoveInput only need a valid Inputs handler.
func newTestFFStream(t *testing.T, ctx context.Context) *FFStream {
	t.Helper()
	s, err := New(ctx)
	require.NoError(t, err)
	return s
}

func TestAddInput_HappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	err := s.AddInput(ctx, Resource{URL: "test://", Priority: 0})
	require.NoError(t, err)

	require.Len(t, s.InputsInfo, 1)
	require.Len(t, s.InputsInfo[0], 1)
	require.Equal(t, "test://", s.InputsInfo[0][0].URL)

	require.GreaterOrEqual(t, len(s.Inputs.InputChains), 1,
		"AddInput must allocate an InputChain at the requested priority")
}

func TestAddInput_DuplicatePriorityReturnsErrInputAlreadyExists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	require.NoError(t, s.AddInput(ctx, Resource{URL: "test://a", Priority: 0}))

	err := s.AddInput(ctx, Resource{URL: "test://b", Priority: 0})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputAlreadyExists),
		"second AddInput at the same priority must wrap ErrInputAlreadyExists, got %v", err)

	require.Len(t, s.InputsInfo[0], 1, "duplicate must not append to the slot")
	require.Equal(t, "test://a", s.InputsInfo[0][0].URL)
}

func TestRemoveInput_HappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	require.NoError(t, s.AddInput(ctx, Resource{URL: "test://", Priority: 0}))

	err := s.RemoveInput(ctx, 0)
	require.NoError(t, err)
	require.Nil(t, s.InputsInfo[0], "InputsInfo slot must be cleared after RemoveInput")
}

func TestRemoveInput_OutOfRange_ReturnsErrInputNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	err := s.RemoveInput(ctx, 99)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputNotFound),
		"out-of-range priority must return ErrInputNotFound, got %v", err)
}

func TestRemoveInput_EmptySlot_ReturnsErrInputNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	require.NoError(t, s.AddInput(ctx, Resource{URL: "test://", Priority: 0}))
	require.NoError(t, s.RemoveInput(ctx, 0))

	err := s.RemoveInput(ctx, 0)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputNotFound),
		"second RemoveInput on the same slot must return ErrInputNotFound, got %v", err)
}

func TestAddInput_AfterRemove_UnpausesChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	// 1. Initial AddInput: allocates the InputChain at priority 0.
	require.NoError(t, s.AddInput(ctx, Resource{URL: "test://1", Priority: 0}))

	// 2. RemoveInput: clears the slot and pauses the chain.
	require.NoError(t, s.RemoveInput(ctx, 0))

	// 3. Re-AddInput at the same priority: hits the int(priority) <
	// preExistingLen branch in AddInput and exercises the Unpause path.
	require.NoError(t, s.AddInput(ctx, Resource{URL: "test://2", Priority: 0}))

	require.Len(t, s.InputsInfo[0], 1)
	require.Equal(t, "test://2", s.InputsInfo[0][0].URL)
}
