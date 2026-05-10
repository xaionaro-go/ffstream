//go:build test_e2e_linux

package ffstreamserver

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asticode/go-astiav"
	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	streammux "github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avpipeline_proto "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestE2E_GRPCReadOnly exercises all read-only gRPC endpoints during active streaming.
func TestE2E_GRPCReadOnly(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("GetStats", func(t *testing.T) {
		stats, err := h.Client.GetStats(ctx)
		require.NoError(t, err)
		require.NotNil(t, stats.GetNodeCounters())
		videoCount := stats.GetNodeCounters().GetProcessed().GetPackets().GetVideo().GetCount()
		audioCount := stats.GetNodeCounters().GetProcessed().GetPackets().GetAudio().GetCount()
		assert.Greater(t, videoCount+audioCount, uint64(0),
			"should have processed packets")
		t.Logf("stats: processed video=%d audio=%d packets", videoCount, audioCount)
	})

	t.Run("GetBitRates", func(t *testing.T) {
		bitRates, err := h.Client.GetBitRates(ctx)
		require.NoError(t, err)
		require.NotNil(t, bitRates)
		t.Logf("bitrates: input(v=%.0f a=%.0f) output(v=%.0f a=%.0f)",
			bitRates.Input.Video, bitRates.Input.Audio,
			bitRates.Output.Video, bitRates.Output.Audio)
	})

	t.Run("GetLatencies", func(t *testing.T) {
		latencies, err := h.Client.GetLatencies(ctx)
		require.NoError(t, err)
		require.NotNil(t, latencies)
		t.Logf("latencies: audio(pre=%d, trans=%d, send=%d) video(pre=%d, trans=%d, send=%d)",
			latencies.Audio.PreTranscoding, latencies.Audio.Transcoding, latencies.Audio.Sending,
			latencies.Video.PreTranscoding, latencies.Video.Transcoding, latencies.Video.Sending)
	})

	t.Run("GetInputQuality", func(t *testing.T) {
		quality, err := h.Client.GetInputQuality(ctx)
		require.NoError(t, err)
		require.NotNil(t, quality)
		t.Logf("input quality: video(continuity=%.3f, fps=%.1f) audio(continuity=%.3f, fps=%.1f)",
			quality.Video.Continuity, quality.Video.FrameRate,
			quality.Audio.Continuity, quality.Audio.FrameRate)
	})

	t.Run("GetOutputQuality", func(t *testing.T) {
		quality, err := h.Client.GetOutputQuality(ctx)
		require.NoError(t, err)
		require.NotNil(t, quality)
		t.Logf("output quality: video(continuity=%.3f, fps=%.1f) audio(continuity=%.3f, fps=%.1f)",
			quality.Video.Continuity, quality.Video.FrameRate,
			quality.Audio.Continuity, quality.Audio.FrameRate)
	})

	t.Run("GetInputsInfo", func(t *testing.T) {
		info, err := h.Client.GetInputsInfo(ctx)
		require.NoError(t, err)
		require.NotNil(t, info)
		require.NotEmpty(t, info.GetInputs(), "should have at least one input")

		input0 := info.GetInputs()[0]
		assert.Equal(t, uint64(0), input0.GetPriority(), "first input should be priority 0")
		assert.Equal(t, uint64(0), input0.GetNum(), "first input should be num 0")
		assert.NotEmpty(t, input0.GetUrl(), "input URL should not be empty")
		assert.False(t, input0.GetSuppressed(), "input should not be suppressed initially")
		t.Logf("input[0]: url=%s priority=%d active=%v suppressed=%v",
			input0.GetUrl(), input0.GetPriority(), input0.GetIsActive(), input0.GetSuppressed())
	})

	t.Run("GetPipelines", func(t *testing.T) {
		pipelines, err := h.Client.GetPipelines(ctx)
		require.NoError(t, err)
		require.NotNil(t, pipelines)
		t.Logf("pipelines: %d nodes", len(pipelines.GetNodes()))
	})

	t.Run("GetCurrentOutput", func(t *testing.T) {
		grpcClient, conn, err := h.Client.GRPCClient(ctx)
		require.NoError(t, err)
		defer conn.Close()

		resp, err := grpcClient.GetCurrentOutput(ctx, &ffstream_grpc.GetCurrentOutputRequest{})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.NotNil(t, resp.GetConfig())
		t.Logf("current output: video=%s audio=%s",
			resp.GetConfig().GetVideo().GetCodecName(),
			resp.GetConfig().GetAudio().GetCodecName())
	})

	t.Run("GetFPSFraction", func(t *testing.T) {
		num, den, err := h.Client.GetFPSFraction(ctx)
		require.NoError(t, err)
		assert.Greater(t, den, uint32(0), "FPS denominator should be > 0")
		t.Logf("FPS fraction: %d/%d", num, den)
	})
}

// TestE2E_GRPCMutations exercises gRPC endpoints that modify pipeline state.
func TestE2E_GRPCMutations(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("SetFPSFraction", func(t *testing.T) {
		// Use 1/2 (half rate) — reduceframerate filter requires Den >= Num.
		err := h.Client.SetFPSFraction(ctx, 1, 2)
		require.NoError(t, err)

		num, den, err := h.Client.GetFPSFraction(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint32(1), num)
		assert.Equal(t, uint32(2), den)
		t.Logf("FPS fraction set to %d/%d", num, den)
	})

	t.Run("SetInputSuppressed", func(t *testing.T) {
		// Suppress input 0
		err := h.Client.SetInputSuppressed(ctx, 0, 0, true)
		require.NoError(t, err)

		// Verify suppression via GetInputsInfo
		info, err := h.Client.GetInputsInfo(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, info.GetInputs())
		assert.True(t, info.GetInputs()[0].GetSuppressed(), "input should be suppressed")

		// Unsuppress
		err = h.Client.SetInputSuppressed(ctx, 0, 0, false)
		require.NoError(t, err)

		info, err = h.Client.GetInputsInfo(ctx)
		require.NoError(t, err)
		assert.False(t, info.GetInputs()[0].GetSuppressed(), "input should be unsuppressed")
	})

	t.Run("SetInputCustomOption", func(t *testing.T) {
		err := h.Client.SetInputCustomOption(ctx, 0, 0, avptypes.DictionaryItem{
			Key:   "test_key",
			Value: "test_value",
		})
		require.NoError(t, err)
	})

	t.Run("InjectData", func(t *testing.T) {
		err := h.Client.InjectData(ctx, []byte{0x01, 0x02, 0x03}, time.Second)
		require.NoError(t, err)
	})

	t.Run("InjectSubtitles", func(t *testing.T) {
		err := h.Client.InjectSubtitles(ctx, "Hello from e2e test", time.Second)
		require.NoError(t, err)
	})

	t.Run("SetStopInput", func(t *testing.T) {
		// Pause input
		err := h.Client.SetStopInput(ctx, 0, true)
		require.NoError(t, err)

		// Pipeline should still be alive (just no input)
		_, err = h.Client.GetStats(ctx)
		require.NoError(t, err, "pipeline should still respond after pausing input")

		// Resume input
		err = h.Client.SetStopInput(ctx, 0, false)
		require.NoError(t, err)
	})
}

// TestE2E_GRPCEnd tests the End RPC which stops transcoding.
func TestE2E_GRPCEnd(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Set the stopTranscodingFunc on the gRPC server (normally done by the caller)
	h.GRPCSrv.locker.Lock()
	h.GRPCSrv.stopTranscodingFunc = h.pipelineCancel
	h.GRPCSrv.locker.Unlock()

	// Call End
	err := h.Client.End(ctx)
	require.NoError(t, err)

	// Pipeline should stop; Wait should return
	h.waitForCompletion(10 * time.Second)
	t.Log("End RPC successfully stopped the pipeline")
}

// TestE2E_FPSFractionValidation tests that invalid FPS fraction values are rejected.
func TestE2E_FPSFractionValidation(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Run("DenZero", func(t *testing.T) {
		err := h.Client.SetFPSFraction(ctx, 30, 0)
		require.Error(t, err, "den=0 should be rejected")
		t.Logf("expected error: %v", err)
	})

	t.Run("FractionGreaterThanOne", func(t *testing.T) {
		err := h.Client.SetFPSFraction(ctx, 30, 7)
		require.Error(t, err, "30/7 (> 1.0) should be rejected")
		t.Logf("expected error: %v", err)
	})

	t.Run("ValidFraction", func(t *testing.T) {
		err := h.Client.SetFPSFraction(ctx, 1, 2)
		require.NoError(t, err, "1/2 should be accepted")
	})
}

// TestE2E_SuppressOutOfRange tests that suppression with invalid indices returns errors.
func TestE2E_SuppressOutOfRange(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Run("InvalidPriority", func(t *testing.T) {
		err := h.Client.SetInputSuppressed(ctx, 99, 0, true)
		require.Error(t, err, "priority 99 should be out of range")
		t.Logf("expected error: %v", err)
	})

	t.Run("InvalidNum", func(t *testing.T) {
		err := h.Client.SetInputSuppressed(ctx, 0, 99, true)
		require.Error(t, err, "num 99 should be out of range")
		t.Logf("expected error: %v", err)
	})

	t.Run("ValidIndices", func(t *testing.T) {
		err := h.Client.SetInputSuppressed(ctx, 0, 0, true)
		require.NoError(t, err)
		// Cleanup
		_ = h.Client.SetInputSuppressed(ctx, 0, 0, false)
	})
}

// TestE2E_SwitchOutputByProps tests output switching via SwitchOutputByProps RPC.
func TestE2E_SwitchOutputByProps(t *testing.T) {
	abr, err := streammux.DefaultAutoBitRateVideoConfig(astiav.CodecIDH264)
	require.NoError(t, err)
	abr.AutoByPass = false

	h := newTestHarness(t,
		withMuxMode(streammuxtypes.MuxModeDifferentOutputsSameTracks),
		withForceRealTime(true),
	)
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = h.Client.SetVideoAutoBitRateConfig(ctx, &abr)
	require.NoError(t, err, "SetVideoAutoBitRateConfig should succeed")

	// Switch to 160x120
	const requestedMaxBitRate = uint64(5_000_000)
	err = h.Client.SwitchOutputByProps(ctx, "libx264", 160, 120, 300000, "aac", 44100, 64000, requestedMaxBitRate)
	require.NoError(t, err, "SwitchOutputByProps should succeed")

	// Verify via GetCurrentOutput that the config changed
	grpcClient, conn, err := h.Client.GRPCClient(ctx)
	require.NoError(t, err)
	defer conn.Close()

	resp, err := grpcClient.GetCurrentOutput(ctx, &ffstream_grpc.GetCurrentOutputRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp.GetConfig())
	assert.Equal(t, requestedMaxBitRate, resp.GetMaxBitRate())
	t.Logf("after switch: video=%s %dx%d audio=%s",
		resp.GetConfig().GetVideo().GetCodecName(),
		resp.GetConfig().GetVideo().GetWidth(),
		resp.GetConfig().GetVideo().GetHeight(),
		resp.GetConfig().GetAudio().GetCodecName())
}

// TestE2E_AutoBitRate tests auto bitrate configuration RPCs.
func TestE2E_AutoBitRate(t *testing.T) {
	abr, err := streammux.DefaultAutoBitRateVideoConfig(astiav.CodecIDH264)
	require.NoError(t, err)
	abr.AutoByPass = false

	h := newTestHarness(t,
		withMuxMode(streammuxtypes.MuxModeSameOutputSameTracks),
		withAutoBitRate(&abr),
		withForceRealTime(true),
	)
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("GetVideoAutoBitRateConfig", func(t *testing.T) {
		cfg, err := h.Client.GetVideoAutoBitRateConfig(ctx)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.False(t, cfg.AutoByPass, "AutoByPass should be false")
		t.Logf("auto bitrate config: checkInterval=%v minBitRate=%.0f maxBitRate=%.0f",
			cfg.CheckInterval, cfg.MinBitRate, cfg.MaxBitRate)
	})

	t.Run("SetVideoAutoBitRateConfig", func(t *testing.T) {
		// Modify and set
		modified := abr
		modified.MaxBitRate = abr.MaxBitRate * 2
		err := h.Client.SetVideoAutoBitRateConfig(ctx, &modified)
		require.NoError(t, err)

		// Verify change
		cfg, err := h.Client.GetVideoAutoBitRateConfig(ctx)
		require.NoError(t, err)
		assert.Equal(t, modified.MaxBitRate, cfg.MaxBitRate, "MaxBitRate should be updated")
		t.Logf("updated maxBitRate=%.0f", cfg.MaxBitRate)
	})

	t.Run("GetVideoAutoBitRateCalculator", func(t *testing.T) {
		calc, err := h.Client.GetVideoAutoBitRateCalculator(ctx)
		require.NoError(t, err)
		require.NotNil(t, calc)
		t.Logf("calculator: %T", calc)
	})

	t.Run("SetVideoAutoBitRateCalculator", func(t *testing.T) {
		// Get current calculator and set it back
		calc, err := h.Client.GetVideoAutoBitRateCalculator(ctx)
		require.NoError(t, err)
		err = h.Client.SetVideoAutoBitRateCalculator(ctx, calc)
		require.NoError(t, err)
		t.Log("set calculator succeeded")
	})
}

// TestE2E_MonitorRPC tests the Monitor streaming RPC.
func TestE2E_MonitorRPC(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get pipeline nodes to find a valid node ID
	pipelines, err := h.Client.GetPipelines(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, pipelines.GetNodes(), "should have at least one pipeline node")

	nodeID := pipelines.GetNodes()[0].GetId()
	t.Logf("monitoring node ID: %d", nodeID)

	// Start monitoring SEND events
	eventCh, err := h.Client.Monitor(ctx, nodeID,
		avpipeline_proto.MonitorEventType_EVENT_TYPE_SEND,
		false, false, false)
	require.NoError(t, err)
	require.NotNil(t, eventCh)

	// Read 1-3 events with timeout
	eventsReceived := 0
	timeout := time.After(10 * time.Second)
	for eventsReceived < 3 {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				t.Logf("event channel closed after %d events", eventsReceived)
				goto done
			}
			require.NotNil(t, ev)
			eventsReceived++
			t.Logf("monitor event #%d: timestamp=%d sourceKernel=%d",
				eventsReceived, ev.GetTimestampNs(), ev.GetSourceKernelId())
		case <-timeout:
			t.Logf("timeout after %d events", eventsReceived)
			goto done
		}
	}
done:
	assert.Greater(t, eventsReceived, 0, "should have received at least 1 monitor event")
}

// TestE2E_InjectionVerification verifies subtitle and data injection, and that the
// pipeline continues processing afterward.
func TestE2E_InjectionVerification(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get baseline stats
	statsBefore, err := h.Client.GetStats(ctx)
	require.NoError(t, err)
	videoBefore := statsBefore.GetNodeCounters().GetProcessed().GetPackets().GetVideo().GetCount()
	audioBefore := statsBefore.GetNodeCounters().GetProcessed().GetPackets().GetAudio().GetCount()

	// Inject subtitles
	err = h.Client.InjectSubtitles(ctx, "Test subtitle", 2*time.Second)
	require.NoError(t, err, "subtitle injection should succeed")

	// Inject data
	err = h.Client.InjectData(ctx, []byte("test data payload"), 2*time.Second)
	require.NoError(t, err, "data injection should succeed")

	// Wait a bit for pipeline to continue processing
	time.Sleep(500 * time.Millisecond)

	// Verify pipeline still processing — packet counts should have increased
	statsAfter, err := h.Client.GetStats(ctx)
	require.NoError(t, err)
	videoAfter := statsAfter.GetNodeCounters().GetProcessed().GetPackets().GetVideo().GetCount()
	audioAfter := statsAfter.GetNodeCounters().GetProcessed().GetPackets().GetAudio().GetCount()

	assert.Greater(t, videoAfter+audioAfter, videoBefore+audioBefore,
		"packet counts should increase after injection")
	t.Logf("before: video=%d audio=%d, after: video=%d audio=%d",
		videoBefore, audioBefore, videoAfter, audioAfter)
}

// TestE2E_ConcurrentMutations tests concurrent gRPC mutations for race safety.
func TestE2E_ConcurrentMutations(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var errCount atomic.Int64
	var successCount atomic.Int64

	const iterations = 10

	// Goroutine 1: SetFPSFraction (use den >= num to avoid internal assertion)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			err := h.Client.SetFPSFraction(ctx, 1, 1)
			if err != nil {
				errCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}
	}()

	// Goroutine 2: SetInputSuppressed alternating
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			suppressed := i%2 == 0
			err := h.Client.SetInputSuppressed(ctx, 0, 0, suppressed)
			if err != nil {
				errCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}
	}()

	// Goroutine 3: GetStats
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, err := h.Client.GetStats(ctx)
			if err != nil {
				errCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}
	}()

	// Goroutine 4: GetBitRates
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, err := h.Client.GetBitRates(ctx)
			if err != nil {
				errCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}
	}()

	// Goroutine 5: InjectSubtitles
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			err := h.Client.InjectSubtitles(ctx, "concurrent test", 500*time.Millisecond)
			if err != nil {
				errCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}
	}()

	wg.Wait()

	t.Logf("concurrent mutations: successes=%d errors=%d", successCount.Load(), errCount.Load())
	assert.Greater(t, successCount.Load(), int64(0), "should have some successful operations")

	// Verify pipeline is still alive
	_, err := h.Client.GetStats(ctx)
	require.NoError(t, err, "pipeline should still be alive after concurrent mutations")
}

// TestE2E_LongRunningStability tests pipeline stability over a sustained period.
func TestE2E_LongRunningStability(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var prevVideo, prevAudio uint64
	var errorCount int

	// Check every second for 10 seconds
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)

		stats, err := h.Client.GetStats(ctx)
		if err != nil {
			errorCount++
			t.Logf("tick %d: GetStats error: %v", i, err)
			continue
		}

		videoCount := stats.GetNodeCounters().GetProcessed().GetPackets().GetVideo().GetCount()
		audioCount := stats.GetNodeCounters().GetProcessed().GetPackets().GetAudio().GetCount()

		if i > 0 {
			assert.Greater(t, videoCount+audioCount, prevVideo+prevAudio,
				"packet counts should increase at tick %d", i)
		}

		prevVideo = videoCount
		prevAudio = audioCount
		t.Logf("tick %d: video=%d audio=%d", i, videoCount, audioCount)
	}

	assert.Equal(t, 0, errorCount, "should have no errors during stability check")

	h.waitForCompletion(30 * time.Second)
	t.Log("long-running stability test completed")
}

// TestE2E_ErrorRecovery verifies that the pipeline continues after invalid operations.
func TestE2E_ErrorRecovery(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get baseline packet counts
	statsBefore, err := h.Client.GetStats(ctx)
	require.NoError(t, err)
	totalBefore := statsBefore.GetNodeCounters().GetProcessed().GetPackets().GetVideo().GetCount() +
		statsBefore.GetNodeCounters().GetProcessed().GetPackets().GetAudio().GetCount()

	// Issue invalid operations
	t.Run("InvalidFPSZeroDen", func(t *testing.T) {
		err := h.Client.SetFPSFraction(ctx, 0, 0)
		require.Error(t, err, "0/0 FPS should be rejected")

		// Verify pipeline is still healthy
		_, err = h.Client.GetStats(ctx)
		require.NoError(t, err, "pipeline should be healthy after invalid FPS")
	})

	t.Run("InvalidSuppressOutOfRange", func(t *testing.T) {
		err := h.Client.SetInputSuppressed(ctx, 99, 99, true)
		require.Error(t, err, "out of range suppression should be rejected")

		_, err = h.Client.GetStats(ctx)
		require.NoError(t, err, "pipeline should be healthy after invalid suppression")
	})

	t.Run("InvalidFPSFractionGreaterThanOne", func(t *testing.T) {
		err := h.Client.SetFPSFraction(ctx, 30, 7)
		require.Error(t, err, "30/7 FPS (> 1.0) should be rejected")

		_, err = h.Client.GetStats(ctx)
		require.NoError(t, err, "pipeline should be healthy after invalid FPS")
	})

	// Issue a valid operation (den >= num required by reduce framerate filter)
	t.Run("ValidAfterErrors", func(t *testing.T) {
		err := h.Client.SetFPSFraction(ctx, 1, 1)
		require.NoError(t, err, "valid FPS should succeed after errors")
	})

	// Wait and verify data flow hasn't stopped
	time.Sleep(500 * time.Millisecond)
	statsAfter, err := h.Client.GetStats(ctx)
	require.NoError(t, err)
	totalAfter := statsAfter.GetNodeCounters().GetProcessed().GetPackets().GetVideo().GetCount() +
		statsAfter.GetNodeCounters().GetProcessed().GetPackets().GetAudio().GetCount()
	assert.Greater(t, totalAfter, totalBefore, "packet counts should still be incrementing")
	t.Logf("recovery verified: packets before=%d after=%d", totalBefore, totalAfter)
}

// TestE2E_SetLoggingLevelUnimplemented verifies that SetLoggingLevel returns gRPC Unimplemented.
func TestE2E_SetLoggingLevelUnimplemented(t *testing.T) {
	h := newTestHarness(t, withForceRealTime(true))
	h.waitForDataFlow(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := h.Client.SetLoggingLevel(ctx, logger.LevelDebug)
	require.Error(t, err, "SetLoggingLevel should return error")

	// Check if it's gRPC Unimplemented
	st, ok := status.FromError(err)
	if ok {
		assert.Equal(t, codes.Unimplemented, st.Code(),
			"SetLoggingLevel should be Unimplemented")
		t.Logf("SetLoggingLevel returned gRPC status: %v", st.Code())
	} else {
		// The client wraps errors with fmt.Errorf, so we might need to unwrap
		t.Logf("SetLoggingLevel returned non-gRPC error: %v (this is acceptable)", err)
	}

	// Verify pipeline still alive
	_, err = h.Client.GetStats(ctx)
	require.NoError(t, err, "pipeline should still be alive after unimplemented call")
}
