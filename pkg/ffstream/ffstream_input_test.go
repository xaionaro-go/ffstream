// ffstream_input_test.go covers the input bookkeeping in
// FFStream.AddInput / FFStream.RemoveInput: priority allocation,
// (priority, num) addressing, sentinel errors, and N-per-priority
// fallback chaining. These tests do not exercise real I/O —
// kernel.NewInput is invoked lazily by the input chain when it is
// actually served, which never happens here.

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

	num, err := s.AddInput(ctx, Resource{URL: "test://", Priority: 0})
	require.NoError(t, err)
	require.Equal(t, uint(0), num,
		"first AddInput at a priority must return num=0")

	require.Len(t, s.InputsInfo, 1)
	require.Len(t, s.InputsInfo[0], 1)
	require.Equal(t, "test://", s.InputsInfo[0][0].URL)

	require.GreaterOrEqual(t, len(s.Inputs.InputChains), 1,
		"AddInput must allocate an InputChain at the requested priority")
}

func TestAddInput_MultipleAtSamePriority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	num0, err := s.AddInput(ctx, Resource{URL: "test://a", Priority: 0})
	require.NoError(t, err)
	require.Equal(t, uint(0), num0)

	num1, err := s.AddInput(ctx, Resource{URL: "test://b", Priority: 0})
	require.NoError(t, err)
	require.Equal(t, uint(1), num1,
		"second AddInput at the same priority must get num=1")

	require.Len(t, s.InputsInfo[0], 2,
		"both Resources must coexist at priority 0")
	require.Equal(t, "test://a", s.InputsInfo[0][0].URL)
	require.Equal(t, "test://b", s.InputsInfo[0][1].URL)

	require.Len(t, s.Inputs.InputChains, 1,
		"adding a second input at the same priority must NOT allocate a new InputChain")
}

func TestRemoveInput_HappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	_, err := s.AddInput(ctx, Resource{URL: "test://", Priority: 0})
	require.NoError(t, err)

	require.NoError(t, s.RemoveInput(ctx, 0, 0))
	require.Empty(t, s.InputsInfo[0],
		"InputsInfo slot must be empty after the only entry is removed")
}

func TestRemoveInput_OutOfRange_ReturnsErrInputNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	err := s.RemoveInput(ctx, 99, 0)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputNotFound),
		"out-of-range priority must return ErrInputNotFound, got %v", err)
}

func TestRemoveInput_EmptySlot_ReturnsErrInputNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	_, err := s.AddInput(ctx, Resource{URL: "test://", Priority: 0})
	require.NoError(t, err)
	require.NoError(t, s.RemoveInput(ctx, 0, 0))

	err = s.RemoveInput(ctx, 0, 0)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputNotFound),
		"second RemoveInput on the same slot must return ErrInputNotFound, got %v", err)
}

func TestRemoveInput_ByPriorityAndNum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)

	num0, err := s.AddInput(ctx, Resource{URL: "test://a", Priority: 0})
	require.NoError(t, err)
	require.Equal(t, uint(0), num0)

	num1, err := s.AddInput(ctx, Resource{URL: "test://b", Priority: 0})
	require.NoError(t, err)
	require.Equal(t, uint(1), num1)

	// Remove the first entry. The second one shifts down to index 0.
	require.NoError(t, s.RemoveInput(ctx, 0, 0))
	require.Len(t, s.InputsInfo[0], 1,
		"RemoveInput must drop exactly one entry from the priority slot")
	require.Equal(t, "test://b", s.InputsInfo[0][0].URL,
		"the surviving entry must be the second one we added")
}

func TestRemoveInput_NumOutOfRange_ReturnsErrInputNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := newTestFFStream(t, ctx)
	_, err := s.AddInput(ctx, Resource{URL: "test://", Priority: 0})
	require.NoError(t, err)

	err = s.RemoveInput(ctx, 0, 5)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInputNotFound),
		"out-of-range num must return ErrInputNotFound, got %v", err)
}
