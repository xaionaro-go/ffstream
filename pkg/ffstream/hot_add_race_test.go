// hot_add_race_test.go pins that a hot AddInput at an already-active
// priority does not race with the in-flight transcoding path through
// the chain's MapStreamIndices and trip an assertion in newOutputStream
// (or surface a data race the -race detector flags). The reproduction
// wires a real lavfi-driven input chain, lets it serve, then issues
// many AddInput calls back-to-back so the Pause+Unpause hot-reload
// sequence collides with concurrent kernel.Generate /
// MapStreamIndices.SendInput work.

package ffstream

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/node"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/observability"
)

// strconvItoaFor is a tiny helper so the stress sibling goroutine
// doesn't pull in fmt at the call site.
func strconvItoaFor(i int) string { return strconv.Itoa(i) }

// TestAddInput_HotReloadRace_NoPanic_NoRace pumps many AddInput calls
// against an actively-serving lavfi chain. Each AddInput at an
// already-occupied priority triggers Pause+Unpause on the chain's
// Retryable, which closes the in-flight kernel and rebuilds it via
// InputFactory.NewInput. The fresh ChainOfTwo carries a brand-new
// MapStreamIndices instance; the in-flight Generate / SendInput
// paths still hold the old one until they wind down. This is the
// window in which the panic was observed.
//
// Pass criteria: -race must not flag a data race; the test must not
// panic on the assertions in
// avpipeline/kernel/map_stream_indices.go newOutputStream.
func TestAddInput_HotReloadRace_NoPanic_NoRace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := New(ctx)
	require.NoError(t, err)

	// Use the same lavfi testsrc shape the existing
	// TestAddInput_AtExistingPriority_RebuildsChain test uses; long
	// enough duration so the chain stays open for the whole stress
	// window.
	res := Resource{
		URL:      "testsrc=duration=99999:rate=25",
		Priority: 0,
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "f", Value: "lavfi"},
			},
		},
	}
	_, err = s.AddInput(ctx, res)
	require.NoError(t, err)
	require.Len(t, s.Inputs.InputChains, 1)

	chain := s.Inputs.InputChains[0]

	// Wire a Passthrough drain so the chain's Generate loop has
	// somewhere to push to. Without a downstream consumer,
	// packetorframe would stall on a backed-up channel and
	// MapStreamIndices.SendInput would never be reached — defeating
	// the repro window.
	sinkNode := node.NewFromKernel(ctx, &kernel.Passthrough{})
	chain.GetOutput().AddPushTo(ctx, sinkNode)

	// Auto-unpause priority 0 to mirror the post-Start startup sequence.
	close(*chain.Input.Processor.Kernel.KernelOpenBarrier.Load())

	// Drive serving in goroutines under an avpipeline.Serve so the
	// nodes pump packets through the full chain (Input → Filter →
	// MapStreamIndices via inputKernel.Generate → sink).
	errCh := make(chan node.Error, 256)
	var serveWG sync.WaitGroup
	serveWG.Add(1)
	observability.Go(ctx, func(ctx context.Context) {
		defer serveWG.Done()
		defer close(errCh)
		nodes := []node.Abstract{chain.Input, chain.Filter, chain.SyncBarrier, sinkNode}
		if chain.AutoHeaders != nil {
			nodes = append(nodes, chain.AutoHeaders)
		}
		if chain.Decoder != nil {
			nodes = append(nodes, chain.Decoder)
		}
		avpipeline.Serve(ctx, avpipeline.ServeConfig{}, errCh, nodes...)
	})
	// Drain pipeline errors so a transient input-open delay doesn't
	// stall the test.
	var errDrainWG sync.WaitGroup
	errDrainWG.Add(1)
	go func() {
		defer errDrainWG.Done()
		for range errCh {
			// Discard. The test's pass criterion is "no panic, no
			// race"; transient open errors are not a failure.
		}
	}()

	// Wait for the kernel to actually open before starting hot-add
	// stress — otherwise the first AddInputs hit IsPaused=true and
	// the fix path skips the Pause+Unpause kick (correct behavior
	// per the existing test, but defeats the repro window).
	require.Eventually(t, func() bool {
		return chain.Input.Processor.Kernel.OriginalPacketSource() != nil
	}, 10*time.Second, 10*time.Millisecond,
		"chain's kernel must open before we begin the hot-reload stress")

	// Hot-add stress: a single goroutine drives AddInput → RemoveInput
	// pairs back-to-back so the (priority, num) pairing stays
	// well-defined (concurrent AddInput at the same priority races the
	// returned num against sibling slices.Delete; that's a test-side
	// invariant, not the bug we're chasing). Sibling goroutines drive
	// other read paths (SetInputCustomOption, GetInputQuality) so the
	// Pause+Unpause hot-reload collides with real concurrent traffic.
	const iterations = 200

	stressCtx, stressCancel := context.WithCancel(ctx)
	defer stressCancel()

	var siblingWG sync.WaitGroup
	siblingWG.Add(1)
	go func() {
		defer siblingWG.Done()
		for i := 0; ; i++ {
			if stressCtx.Err() != nil {
				return
			}
			_ = s.SetInputCustomOption(stressCtx, 0, 0, "stress_key",
				strconvItoaFor(i))
		}
	}()

	for i := 0; i < iterations; i++ {
		newRes := Resource{
			URL:      "testsrc=duration=99999:rate=25",
			Priority: 0,
			InputConfig: kernel.InputConfig{
				CustomOptions: avptypes.DictionaryItems{
					{Key: "f", Value: "lavfi"},
				},
			},
		}
		num, err := s.AddInput(ctx, newRes)
		require.NoErrorf(t, err, "AddInput #%d", i)
		require.NoErrorf(t, s.RemoveInput(ctx, 0, num),
			"RemoveInput #%d num=%d", i, num)
	}
	stressCancel()
	siblingWG.Wait()

	// Stop serving and wait for goroutines to wind down.
	cancel()
	serveWG.Wait()
	errDrainWG.Wait()
}
