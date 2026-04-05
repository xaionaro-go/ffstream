// bug_fixes_test.go contains regression tests for BUG-004, BUG-005, BUG-007,
// BUG-008, and BUG-009 — boundary and race conditions around InputsInfo
// mutation and stream-index mapping.

package ffstream

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/asticode/go-astiav"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/packet"
	"github.com/xaionaro-go/avpipeline/packetorframe"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

// testStreamSource is a minimal packet.Source implementation for tests that
// only need a non-nil AbstractSource value.
type testStreamSource struct {
	name string
}

func (t *testStreamSource) String() string { return t.name }
func (t *testStreamSource) WithOutputFormatContext(
	_ context.Context,
	_ func(*astiav.FormatContext),
) {
}

// makeTestInputUnion builds a packetorframe.InputUnion backed by a packet.Input
// with a real astiav.Packet (so GetStreamIndex is well-defined on Packet), a
// non-nil source, and a ResourceIndex in pipeline side data. The resulting
// InputUnion supports GetStreamIndex / GetSource / GetMediaType /
// GetPipelineSideData — enough for streamIndexAssignLocked and the boundary
// paths in shouldForwardToOutput / onStreamMapped.
func makeTestInputUnion(
	t *testing.T,
	src packet.Source,
	streamIdx int,
	resourceIdx ResourceIndex,
) packetorframe.InputUnion {
	t.Helper()
	pkt := astiav.AllocPacket()
	t.Cleanup(pkt.Free)
	pkt.SetStreamIndex(streamIdx)
	si := &packet.StreamInfo{
		Source:           src,
		StreamIndex:      streamIdx,
		PipelineSideData: avptypes.PipelineSideData{resourceIdx},
	}
	p := packet.BuildInput(pkt, si)
	return packetorframe.InputUnion{Packet: &p}
}

// TestBUG007_InputFactory_StreamIndexAssign_FreshFactoryDoesNotPanic proves
// that calling streamIndexAssignLocked on a freshly-constructed factory does
// not panic due to an uninitialized streamIndexMap. Before the fix, the map
// was only initialized in NewInput at the first kernel creation, so a direct
// call to StreamIndexAssign triggered a "assignment to entry in nil map"
// runtime panic.
func TestBUG007_InputFactory_StreamIndexAssign_FreshFactoryDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	s := &FFStream{}
	f := newInputFactory(s, 0)

	src := &testStreamSource{name: "test-src"}

	// srcIdx=0,streamIdx=0 is short-circuited before the map write — use
	// non-zero values so the code path reaches `f.streamIndexMap[key] = out`.
	in := makeTestInputUnion(t, src, 7, 3)

	require.NotPanics(t, func() {
		_, _ = f.streamIndexAssignLocked(ctx, in)
	}, "streamIndexAssignLocked must not panic on a freshly-constructed factory")

	// The initialized map must also be usable afterwards: a second call with
	// the same key must hit the cache path and return the same index.
	out1, err := f.streamIndexAssignLocked(ctx, in)
	require.NoError(t, err)
	require.Len(t, out1, 1)

	out2, err := f.streamIndexAssignLocked(ctx, in)
	require.NoError(t, err)
	require.Equal(t, out1, out2, "same key must return the cached index")
}

// TestBUG005_FFStream_OnStreamMapped_OutOfRangePriority ensures onStreamMapped
// does not index InputsInfo[priority][resourceIdx] when priority is outside
// the slice. Before the fix, the access caused an index-out-of-range panic.
func TestBUG005_FFStream_OnStreamMapped_OutOfRangePriority(t *testing.T) {
	ctx := context.Background()

	s := &FFStream{
		InputsInfo:         []Resources{{{}}}, // priority 0 has 1 resource
		audioStreamIndices: make(map[fallbackResourceKey]int),
	}

	require.NotPanics(t, func() {
		s.onStreamMapped(ctx, 5, 0, astiav.MediaTypeAudio, 42)
	}, "onStreamMapped must not panic when priority is out of range")
}

// TestBUG005_FFStream_OnStreamMapped_OutOfRangeResourceIdx ensures
// onStreamMapped does not index InputsInfo[priority][resourceIdx] when
// resourceIdx is outside the inner slice.
func TestBUG005_FFStream_OnStreamMapped_OutOfRangeResourceIdx(t *testing.T) {
	ctx := context.Background()

	s := &FFStream{
		InputsInfo:         []Resources{{{}}}, // priority 0 has 1 resource
		audioStreamIndices: make(map[fallbackResourceKey]int),
	}

	require.NotPanics(t, func() {
		s.onStreamMapped(ctx, 0, 5, astiav.MediaTypeAudio, 42)
	}, "onStreamMapped must not panic when resourceIdx is out of range")
}

// TestBUG005_FFStream_OnStreamMapped_ValidIndicesStillWorks is the dual-sided
// positive check: valid indices must still record the audio stream mapping.
func TestBUG005_FFStream_OnStreamMapped_ValidIndicesStillWorks(t *testing.T) {
	ctx := context.Background()

	s := &FFStream{
		InputsInfo: []Resources{
			{
				{}, // idx 0
				{}, // idx 1
			},
		},
		audioStreamIndices: make(map[fallbackResourceKey]int),
	}

	s.onStreamMapped(ctx, 0, 1, astiav.MediaTypeAudio, 42)

	key := fallbackResourceKey{Priority: 0, ResourceIdx: 1}
	got, ok := s.audioStreamIndices[key]
	assert.True(t, ok, "valid (priority, resourceIdx) must record the mapping")
	assert.Equal(t, 42, got)
}

// TestBUG004_DecoderFactory_LookupResource_OutOfRange covers the helper that
// replaces the unchecked InputsInfo[priority][resourceIdx] access in
// NewDecoder. Before the fix, an out-of-range resourceIndex (or priority)
// would index-out-of-range at that line.
func TestBUG004_DecoderFactory_LookupResource_OutOfRange(t *testing.T) {
	s := &FFStream{
		InputsInfo: []Resources{
			{{URL: "a"}}, // priority 0 has 1 resource
		},
	}
	f := &DecoderFactory{
		InputFactory: &InputFactory{
			FFStream:         s,
			FallbackPriority: 0,
		},
	}

	t.Run("resourceIndex out of range", func(t *testing.T) {
		_, ok := f.lookupResource(ResourceIndex(5))
		assert.False(t, ok, "out-of-range resourceIndex must report ok=false, not panic")
	})

	t.Run("priority out of range", func(t *testing.T) {
		f2 := &DecoderFactory{
			InputFactory: &InputFactory{
				FFStream:         s,
				FallbackPriority: 9,
			},
		}
		_, ok := f2.lookupResource(ResourceIndex(0))
		assert.False(t, ok, "out-of-range priority must report ok=false, not panic")
	})

	t.Run("negative resourceIndex", func(t *testing.T) {
		_, ok := f.lookupResource(ResourceIndex(-1))
		assert.False(t, ok, "negative resourceIndex must report ok=false, not panic")
	})

	t.Run("valid index returns resource", func(t *testing.T) {
		r, ok := f.lookupResource(ResourceIndex(0))
		require.True(t, ok)
		assert.Equal(t, "a", r.URL)
	})
}

// TestBUG004_DecoderFactory_LookupResource_TakesLocker verifies the lookup
// holds FFStream.locker while reading. The test holds FFStream.locker in the
// main goroutine, fires off a goroutine that calls lookupResource, and waits
// a bounded period for completion. If lookupResource did not take the locker,
// it would return immediately; with the locker held, it must remain blocked
// until the main goroutine releases. The timing-based block check is
// sufficient because the call site is otherwise trivial (a few compares and
// a slice read).
func TestBUG004_DecoderFactory_LookupResource_TakesLocker(t *testing.T) {
	s := &FFStream{
		InputsInfo: []Resources{
			{{URL: "a"}},
		},
	}
	f := &DecoderFactory{
		InputFactory: &InputFactory{
			FFStream:         s,
			FallbackPriority: 0,
		},
	}

	s.locker.Lock()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		_, _ = f.lookupResource(ResourceIndex(0))
		close(done)
	}()

	// Wait for the goroutine to at least start.
	<-started
	// Give the goroutine enough time to complete if it were not blocking.
	// 50ms is orders of magnitude longer than an uncontested slice read.
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-done:
		s.locker.Unlock()
		t.Fatal("lookupResource must block while FFStream.locker is held by another goroutine")
	case <-timer.C:
	}

	s.locker.Unlock()
	<-done
}

// TestBUG008_AddInput_InvariantHoldsOnAddFactoryFailure exercises the rollback
// behavior of AddInput. A freshly-initialized FFStream with 0 input chains
// accepts AddInput with priority 0 cleanly. Adding a second resource at the
// same priority must leave InputsInfo and InputChains in sync. The test
// asserts the invariant len(InputsInfo) == len(InputChains) holds both on
// success and remains consistent across multiple calls.
func TestBUG008_AddInput_InvariantHolds(t *testing.T) {
	ctx := context.Background()

	s, err := New(ctx)
	require.NoError(t, err)

	// Baseline: fresh FFStream has 0 chains and 0 InputsInfo.
	assert.Equal(t, 0, len(s.InputsInfo))
	assert.Equal(t, 0, s.Inputs.GetInputChainsCount(ctx))

	// Add a resource at priority 0.
	err = s.AddInput(ctx, Resource{URL: "file:/does-not-exist-1"})
	require.NoError(t, err)
	require.Equal(t, len(s.InputsInfo), s.Inputs.GetInputChainsCount(ctx),
		"invariant len(InputsInfo) == len(InputChains) must hold after AddInput")
	assert.Equal(t, 1, len(s.InputsInfo))
	assert.Len(t, s.InputsInfo[0], 1)

	// Add another resource at the same priority: no new chain, just append.
	err = s.AddInput(ctx, Resource{URL: "file:/does-not-exist-2"})
	require.NoError(t, err)
	require.Equal(t, len(s.InputsInfo), s.Inputs.GetInputChainsCount(ctx))
	assert.Equal(t, 1, len(s.InputsInfo))
	assert.Len(t, s.InputsInfo[0], 2)

	// Add a resource at priority 2 (skipping priority 1): grows both slices
	// by 2 (priorities 1 and 2).
	err = s.AddInput(ctx, Resource{
		URL: "file:/does-not-exist-3",
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{{Key: "fallback_priority", Value: "2"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, len(s.InputsInfo), s.Inputs.GetInputChainsCount(ctx),
		"invariant must hold even after growing by multiple priority levels")
	assert.Equal(t, 3, len(s.InputsInfo))
	assert.Len(t, s.InputsInfo[0], 2)
	assert.Nil(t, s.InputsInfo[1])
	assert.Len(t, s.InputsInfo[2], 1)
}

// TestBUG008_AddInput_ConcurrentReadDuringGrowth ensures that reading
// InputsInfo via methods that take s.locker does not race with AddInput's
// growth logic. This is a smoke-test; it runs reliably only with -race.
func TestBUG008_AddInput_ConcurrentReadDuringGrowth(t *testing.T) {
	ctx := context.Background()

	s, err := New(ctx)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = s.SetSuppressed(ctx, 0, 0, i%2 == 0)
		}
	}()

	go func() {
		defer wg.Done()
		for range 20 {
			_ = s.AddInput(ctx, Resource{URL: "file:/no"})
		}
	}()

	wg.Wait()
	// Invariant must still hold at the end.
	assert.Equal(t, len(s.InputsInfo), s.Inputs.GetInputChainsCount(ctx))
}

// TestBUG008_AddInput_RollbackKeepsInvariantOnFailure creates a scenario
// where AddFactory fails AFTER having appended an InputChain. We do this by
// filling the newInputChainChan on the inputwithfallback until it overflows,
// which triggers the `default:` branch in addFactory that returns an error
// after the append. Before the fix, this would leave
// len(InputsInfo) < len(InputChains) because the error rolled InputsInfo back
// to startLen. After the fix, the invariant is preserved.
func TestBUG008_AddInput_RollbackKeepsInvariantOnFailure(t *testing.T) {
	ctx := context.Background()

	s, err := New(ctx)
	require.NoError(t, err)

	// Fill the InputWithFallback.newInputChainChan (buffered to 100) by
	// adding resources at distinct priorities. Each successful AddInput
	// appends one entry to the channel and that entry is never drained
	// (because avpipeline.Serve is not running in this test). At priority
	// 100 (the 101st chain), the append succeeds but the channel send
	// hits the `default:` branch and AddFactory returns an error.
	for p := range 100 {
		err := s.AddInput(ctx, Resource{
			URL: "file:/does-not-exist",
			InputConfig: kernel.InputConfig{
				CustomOptions: avptypes.DictionaryItems{
					{Key: "fallback_priority", Value: strconv.Itoa(p)},
				},
			},
		})
		require.NoError(t, err, "priority %d", p)
	}
	require.Equal(t, len(s.InputsInfo), s.Inputs.GetInputChainsCount(ctx),
		"invariant must hold at 100 chains")

	// The 101st AddInput overflows the channel and triggers the failure
	// path in addFactory: append happened, channel send fails, the
	// inputChain is Closed, and an error is returned.
	err = s.AddInput(ctx, Resource{
		URL: "file:/does-not-exist",
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "fallback_priority", Value: "100"},
			},
		},
	})
	require.Error(t, err, "expected AddInput to fail when newInputChainChan is full")

	// Invariant: len(InputsInfo) == len(InputChains) must hold even after
	// a failed AddInput.
	assert.Equal(t, s.Inputs.GetInputChainsCount(ctx), len(s.InputsInfo),
		"len(InputsInfo) must equal len(InputChains) after failed AddInput")
}

// TestBUG008_AddInput_MidLoopPartialFailure exercises the mid-loop failure
// path of AddInput, where the priority-grow loop runs MULTIPLE iterations
// and the LAST iteration fails after InputChains has grown. With the
// alignment-loop fix (newCount := GetInputChainsCount ; align InputsInfo),
// the invariant len(InputsInfo) == len(InputChains) is preserved. With the
// old startLen-based rollback, InputsInfo would be truncated to the value
// it had BEFORE the first successful iteration, dropping the entries the
// successful iterations created and breaking the invariant.
//
// The failure is arranged via the inputwithfallback.newInputChainChan
// capacity: 99 successful AddInput calls leave the channel at 99/100, so a
// single AddInput(priority=100) iterates p=99 (succeeds, channel -> 100)
// and p=100 (InputChains +1 before the full-channel error, so AddFactory
// returns an error AFTER its internal append).
func TestBUG008_AddInput_MidLoopPartialFailure(t *testing.T) {
	ctx := context.Background()

	s, err := New(ctx)
	require.NoError(t, err)

	// Fill 99 slots so the channel has 99/100 items queued and no
	// iteration of the AddInput loop below has been triggered yet.
	for p := range 99 {
		err := s.AddInput(ctx, Resource{
			URL: "file:/does-not-exist",
			InputConfig: kernel.InputConfig{
				CustomOptions: avptypes.DictionaryItems{
					{Key: "fallback_priority", Value: strconv.Itoa(p)},
				},
			},
		})
		require.NoError(t, err, "pre-fill priority %d", p)
	}
	require.Equal(t, 99, s.Inputs.GetInputChainsCount(ctx))
	require.Equal(t, 99, len(s.InputsInfo))

	// A single AddInput(priority=100) iterates p=99 and p=100. p=99
	// succeeds (channel 99 -> 100). p=100 overflows the channel and
	// fails AFTER AddFactory has internally appended to InputChains
	// (so InputChains goes 100 -> 101 before the error is returned).
	// Prior to the fix, the rollback would truncate InputsInfo back to
	// startLen (99), leaving len(InputsInfo)=99 but len(InputChains)=101
	// — violating the invariant.
	err = s.AddInput(ctx, Resource{
		URL: "file:/does-not-exist",
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "fallback_priority", Value: "100"},
			},
		},
	})
	require.Error(t, err, "AddInput(priority=100) must fail — channel is full")

	// The loop must have run BOTH iterations: p=99 succeeded (so the
	// InputChains count grew from 99 to at least 100), and p=100 failed
	// after a partial append (InputChains is 101). If InputChains is
	// only 100, the failing iteration did NOT partial-append and this
	// test is not exercising the mid-loop path.
	chains := s.Inputs.GetInputChainsCount(ctx)
	require.GreaterOrEqual(t, chains, 100,
		"p=99 must have grown InputChains to at least 100 before p=100 failed")
	require.Equal(t, 101, chains,
		"p=100 must have partial-appended an InputChain (101 total) before the full-channel error")

	// Invariant: len(InputsInfo) == len(InputChains) even after the
	// mid-loop failure.
	assert.Equal(t, chains, len(s.InputsInfo),
		"len(InputsInfo) must equal len(InputChains) after mid-loop partial failure")
}

// TestBUG009_FFStream_SetInputCustomOption_TakesLocker verifies that
// SetInputCustomOption acquires FFStream.locker. We assert by pre-acquiring
// the lock from the main goroutine, firing off a call from a sub-goroutine,
// and confirming the sub-goroutine remains blocked until the lock is
// released.
func TestBUG009_FFStream_SetInputCustomOption_TakesLocker(t *testing.T) {
	ctx := context.Background()

	s := &FFStream{
		InputsInfo: []Resources{
			{{URL: "a"}},
		},
	}

	s.locker.Lock()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		_ = s.SetInputCustomOption(ctx, 0, 0, "f", "mpegts")
		close(done)
	}()

	<-started
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-done:
		s.locker.Unlock()
		t.Fatal("SetInputCustomOption must block while FFStream.locker is held by another goroutine")
	case <-timer.C:
	}

	s.locker.Unlock()
	<-done

	// Verify the mutation reached the slice after the lock is released.
	got := s.InputsInfo[0][0].CustomOptions.GetFirst("f")
	require.NotNil(t, got)
	assert.Equal(t, "mpegts", *got)
}

// TestBUG009_FFStream_SetInputCustomOption_ConcurrentMutation exercises the
// concurrent read-modify-write path to smoke-test that SetInputCustomOption
// is safe against concurrent invocations of itself and SetSuppressed.
func TestBUG009_FFStream_SetInputCustomOption_ConcurrentMutation(t *testing.T) {
	ctx := context.Background()

	s, err := New(ctx)
	require.NoError(t, err)

	err = s.AddInput(ctx, Resource{URL: "file:/does-not-exist"})
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = s.SetInputCustomOption(ctx, 0, 0, "f", "mpegts")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = s.SetInputCustomOption(ctx, 0, 0, "buffer_size", "1024")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = s.SetSuppressed(ctx, 0, 0, i%2 == 0)
		}
	}()

	wg.Wait()
}

// TestBUG009_FFStream_SetInputCustomOption_BoundsChecks verifies that
// out-of-range priority or num are rejected cleanly.
func TestBUG009_FFStream_SetInputCustomOption_BoundsChecks(t *testing.T) {
	ctx := context.Background()

	s := &FFStream{
		InputsInfo: []Resources{
			{{URL: "a"}},
		},
	}

	t.Run("priority out of range", func(t *testing.T) {
		err := s.SetInputCustomOption(ctx, 5, 0, "k", "v")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})

	t.Run("num out of range", func(t *testing.T) {
		err := s.SetInputCustomOption(ctx, 0, 5, "k", "v")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})

	t.Run("valid indices mutate the resource", func(t *testing.T) {
		err := s.SetInputCustomOption(ctx, 0, 0, "f", "mpegts")
		require.NoError(t, err)
		got := s.InputsInfo[0][0].CustomOptions.GetFirst("f")
		require.NotNil(t, got)
		assert.Equal(t, "mpegts", *got)
	})
}
