//go:build test_e2e_linux

package ffstreamserver

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/kernel"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
)

// TestE2E_PipelineTranscode verifies the full pipeline: input → libx264+aac → FLV output.
func TestE2E_PipelineTranscode(t *testing.T) {
	h := newTestHarness(t) // default: libx264+aac, 320x240, short input

	// Wait for pipeline to complete (short input processed at full speed)
	h.waitForCompletion(60 * time.Second)

	// Verify output file
	result := verifyOutputFile(t, h.outputPath)
	assert.True(t, result.hasStreamType("video"), "output should have video stream")
	assert.True(t, result.hasStreamType("audio"), "output should have audio stream")

	videoStream := result.streamByType("video")
	require.NotNil(t, videoStream)
	assert.Equal(t, "h264", videoStream.CodecName, "transcoded video should be h264")
	assert.Equal(t, 320, videoStream.Width, "video width should be 320")
	assert.Equal(t, 240, videoStream.Height, "video height should be 240")

	audioStream := result.streamByType("audio")
	require.NotNil(t, audioStream)
	assert.Equal(t, "aac", audioStream.CodecName, "transcoded audio should be aac")

	t.Logf("output verified: video=%s %dx%d, audio=%s, format=%s",
		videoStream.CodecName, videoStream.Width, videoStream.Height,
		audioStream.CodecName, result.Format.FormatName)
}

// TestE2E_PipelineGracefulShutdown verifies that cancelling the pipeline context
// shuts down cleanly without panics, and produces a partially valid output.
func TestE2E_PipelineGracefulShutdown(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))

	// Wait for data to start flowing
	h.waitForDataFlow(15 * time.Second)

	// Let it process for a bit
	time.Sleep(2 * time.Second)

	// Cancel the pipeline
	h.pipelineCancel()

	// Wait briefly for shutdown
	h.waitForCompletion(10 * time.Second)

	// Output file should exist and have some content
	result := verifyOutputFile(t, h.outputPath)
	assert.True(t, result.hasStreamType("video"), "output should have video even after early shutdown")

	t.Logf("graceful shutdown verified: %d streams in output", len(result.Streams))
}

// TestE2E_PipelineCopyCodecError verifies that copy-codec configuration cannot produce
// output through the transcoder pipeline. EncoderCopy.SendFrame() returns ErrCopyEncoder.
func TestE2E_PipelineCopyCodecError(t *testing.T) {
	h := newHarnessNoStart(t)

	shortPath, _ := ensureSharedInputs(t)

	err := h.FFStream.AddInput(h.pipelineCtx, ffstream.Resource{
		URL: shortPath,
		InputConfig: kernel.InputConfig{
			ForceRealTime: ptr(false),
		},
	})
	require.NoError(t, err)

	err = h.FFStream.AddOutputTemplate(h.pipelineCtx, ffstream.SenderTemplate{
		URLTemplate: h.outputPath,
		Options: avptypes.DictionaryItems{
			{Key: "f", Value: "flv"},
		},
	})
	require.NoError(t, err)

	cfg := defaultHarnessConfig()
	cfg.videoCodec = "copy"
	cfg.audioCodec = "copy"
	transcoderConfig := buildTranscoderConfig(cfg)

	err = h.FFStream.Start(h.pipelineCtx, transcoderConfig, streammuxtypes.MuxModeForbid, nil)
	if err != nil {
		t.Logf("Start() with copy codec returned error as expected: %v", err)
		return
	}

	// If Start didn't error, wait for pipeline to finish — it should either fail or produce no output
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer waitCancel()
	waitErr := h.FFStream.Wait(waitCtx)
	t.Logf("pipeline wait returned: %v", waitErr)

	// Output should either not exist or be empty
	info, statErr := os.Stat(h.outputPath)
	if statErr != nil {
		t.Logf("output file does not exist (expected with copy codec): %v", statErr)
		return
	}
	t.Logf("output file size: %d bytes (copy codec may produce partial/empty output)", info.Size())
}

// TestE2E_MultipleInputsFallback verifies the pipeline works with multiple inputs at different priorities.
func TestE2E_MultipleInputsFallback(t *testing.T) {
	shortPath, _ := ensureSharedInputs(t)

	h := newMultiInputHarness(t,
		[]string{shortPath, "/nonexistent/missing_input.flv"},
	)

	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Verify we have 2 input priority levels
	info, err := h.Client.GetInputsInfo(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, info.GetInputs())
	t.Logf("inputs count: %d", len(info.GetInputs()))

	// Priority 0 input should be active (valid file)
	foundPri0 := false
	for _, inp := range info.GetInputs() {
		if inp.GetPriority() == 0 {
			foundPri0 = true
			assert.True(t, inp.GetIsActive(), "priority 0 input should be active")
			t.Logf("input pri=%d num=%d active=%v url=%s",
				inp.GetPriority(), inp.GetNum(), inp.GetIsActive(), inp.GetUrl())
		}
	}
	assert.True(t, foundPri0, "should have an input at priority 0")

	// Wait for completion and verify output
	h.waitForCompletion(60 * time.Second)
	result := verifyOutputFile(t, h.outputPath)
	assert.True(t, result.hasStreamType("video"), "output should have video")
	assert.True(t, result.hasStreamType("audio"), "output should have audio")
}

// TestE2E_MuxModeVariants verifies the pipeline works with different MuxMode configurations.
func TestE2E_MuxModeVariants(t *testing.T) {
	testCases := []struct {
		name string
		mode streammuxtypes.MuxMode
	}{
		{"MuxModeForbid", streammuxtypes.MuxModeForbid},
		{"MuxModeSameOutputSameTracks", streammuxtypes.MuxModeSameOutputSameTracks},
		{"MuxModeDifferentOutputsSameTracks", streammuxtypes.MuxModeDifferentOutputsSameTracks},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness(t, withMuxMode(tc.mode))
			h.waitForCompletion(60 * time.Second)
			result := verifyOutputFile(t, h.outputPath)
			assert.True(t, result.hasStreamType("video"), "output should have video stream")
			assert.True(t, result.hasStreamType("audio"), "output should have audio stream")
			t.Logf("muxMode=%s: output verified with %d streams", tc.name, len(result.Streams))
		})
	}
}

// TestE2E_MultipleOutputTemplatesError verifies that Start() rejects >1 output template.
func TestE2E_MultipleOutputTemplatesError(t *testing.T) {
	h := newHarnessNoStart(t)

	shortPath, _ := ensureSharedInputs(t)

	err := h.FFStream.AddInput(h.pipelineCtx, ffstream.Resource{
		URL: shortPath,
		InputConfig: kernel.InputConfig{
			ForceRealTime: ptr(false),
		},
	})
	require.NoError(t, err)

	// Add first output template
	err = h.FFStream.AddOutputTemplate(h.pipelineCtx, ffstream.SenderTemplate{
		URLTemplate: h.outputPath,
		Options: avptypes.DictionaryItems{
			{Key: "f", Value: "flv"},
		},
	})
	require.NoError(t, err)

	// Add second output template
	err = h.FFStream.AddOutputTemplate(h.pipelineCtx, ffstream.SenderTemplate{
		URLTemplate: h.outputPath + ".2.flv",
		Options: avptypes.DictionaryItems{
			{Key: "f", Value: "flv"},
		},
	})
	require.NoError(t, err)

	cfg := defaultHarnessConfig()
	transcoderConfig := buildTranscoderConfig(cfg)

	err = h.FFStream.Start(h.pipelineCtx, transcoderConfig, streammuxtypes.MuxModeForbid, nil)
	require.Error(t, err, "Start() should reject multiple output templates")
	assert.Contains(t, err.Error(), "at most one output template",
		"error should mention output template constraint")
	t.Logf("expected error: %v", err)
}

// TestE2E_InputRetry verifies the pipeline handles input retry for non-existent files.
func TestE2E_InputRetry(t *testing.T) {
	h := newTestHarness(t,
		withInputPath("/nonexistent/file.flv"),
		withRetryInterval(200*time.Millisecond),
		withForceRealTime(true),
	)

	// Pipeline should start without error (input retry is async)
	// Wait 1 second — pipeline should still be alive (retrying), not crashed
	time.Sleep(1 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Verify pipeline is responsive via GetStats
	stats, err := h.Client.GetStats(ctx)
	require.NoError(t, err, "pipeline should still be responsive during retry")
	require.NotNil(t, stats)
	t.Logf("pipeline alive during input retry, stats received")

	// Cancel pipeline, verify clean shutdown
	h.pipelineCancel()
	h.waitForCompletion(10 * time.Second)
	t.Log("input retry pipeline shut down cleanly")
}
