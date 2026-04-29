//go:build test_e2e_linux

package ffstreamserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	audio "github.com/xaionaro-go/audio/pkg/audio/types"
	"github.com/xaionaro-go/avpipeline/codec"
	codectypes "github.com/xaionaro-go/avpipeline/codec/types"
	"github.com/xaionaro-go/avpipeline/kernel"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/client"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"google.golang.org/grpc"
)

// --- Shared test input ---

var (
	sharedInputOnce     sync.Once
	sharedInputDir      string
	sharedInputShort    string // 5-second test input
	sharedInputLong     string // 15-second test input
	sharedInputSetupErr error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedInputDir != "" {
		os.RemoveAll(sharedInputDir)
	}
	os.Exit(code)
}

func ensureSharedInputs(t *testing.T) (shortPath, longPath string) {
	t.Helper()
	sharedInputOnce.Do(func() {
		sharedInputDir, sharedInputSetupErr = os.MkdirTemp("", "ffstream-e2e-*")
		if sharedInputSetupErr != nil {
			return
		}
		sharedInputShort, sharedInputSetupErr = generateTestInput(sharedInputDir, "input_short.flv", 5)
		if sharedInputSetupErr != nil {
			return
		}
		sharedInputLong, sharedInputSetupErr = generateTestInput(sharedInputDir, "input_long.flv", 15)
	})
	if sharedInputSetupErr != nil {
		t.Skipf("unable to generate test inputs: %v", sharedInputSetupErr)
	}
	return sharedInputShort, sharedInputLong
}

func generateTestInput(dir, name string, durationSec int) (string, error) {
	outputPath := filepath.Join(dir, name)
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=duration=%d:size=320x240:rate=15", durationSec),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%d:sample_rate=44100", durationSec),
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "64k",
		"-shortest",
		"-f", "flv",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ffmpeg failed: %w\n%s", err, out)
	}
	return outputPath, nil
}

// --- Pipeline harness ---

type testHarness struct {
	t              *testing.T
	tempDir        string
	outputPath     string
	FFStream       *ffstream.FFStream
	GRPCServer     *grpc.Server
	GRPCSrv        *GRPCServer
	Client         *client.Client
	pipelineCtx    context.Context
	pipelineCancel context.CancelFunc
}

type harnessConfig struct {
	inputPath     string
	forceRealTime bool
	videoCodec    string // encoder name like "libx264"
	audioCodec    string // encoder name like "aac"
	resolution    codec.Resolution
	sampleRate    audio.SampleRate
	muxMode       streammuxtypes.MuxMode
	autoBitRate   *streammuxtypes.AutoBitRateVideoConfig
	retryInterval time.Duration // -1 = no retry (default), >0 = retry interval
}

func defaultHarnessConfig() harnessConfig {
	return harnessConfig{
		videoCodec:    "libx264",
		audioCodec:    "aac",
		resolution:    codec.Resolution{Width: 320, Height: 240},
		sampleRate:    audio.SampleRate(44100),
		muxMode:       streammuxtypes.MuxModeForbid,
		retryInterval: -1,
	}
}

type harnessOption func(*harnessConfig)

func withForceRealTime(v bool) harnessOption {
	return func(c *harnessConfig) { c.forceRealTime = v }
}

func withVideoCodec(codec string) harnessOption {
	return func(c *harnessConfig) { c.videoCodec = codec }
}

func withAudioCodec(codec string) harnessOption {
	return func(c *harnessConfig) { c.audioCodec = codec }
}

func withMuxMode(mode streammuxtypes.MuxMode) harnessOption {
	return func(c *harnessConfig) { c.muxMode = mode }
}

func withAutoBitRate(cfg *streammuxtypes.AutoBitRateVideoConfig) harnessOption {
	return func(c *harnessConfig) { c.autoBitRate = cfg }
}

func withRetryInterval(d time.Duration) harnessOption {
	return func(c *harnessConfig) { c.retryInterval = d }
}

func withInputPath(path string) harnessOption {
	return func(c *harnessConfig) { c.inputPath = path }
}

func newTestHarness(t *testing.T, opts ...harnessOption) *testHarness {
	t.Helper()

	cfg := defaultHarnessConfig()
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.inputPath == "" {
		shortPath, longPath := ensureSharedInputs(t)
		if cfg.forceRealTime {
			cfg.inputPath = longPath
		} else {
			cfg.inputPath = shortPath
		}
	}

	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "output.flv")

	ctx := context.Background()
	pipeCtx, pipeCancel := context.WithCancel(ctx)

	s, err := ffstream.New(pipeCtx, ffstream.OptionInputRetryInterval(cfg.retryInterval))
	require.NoError(t, err)

	err = s.AddInput(pipeCtx, ffstream.Resource{
		URL: cfg.inputPath,
		InputConfig: kernel.InputConfig{
			ForceRealTime: ptr(cfg.forceRealTime),
		},
	})
	require.NoError(t, err)

	err = s.AddOutputTemplate(pipeCtx, ffstream.SenderTemplate{
		URLTemplate: outputPath,
		Options: avptypes.DictionaryItems{
			{Key: "f", Value: "flv"},
		},
	})
	require.NoError(t, err)

	// Setup gRPC server
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	grpcServer := grpc.NewServer()
	grpcSrv := NewGRPCServer(pipeCtx, s)
	ffstream_grpc.RegisterFFStreamServer(grpcServer, grpcSrv)
	go func() { _ = grpcServer.Serve(lis) }()

	c := client.New(lis.Addr().String())

	// Build transcoder config
	transcoderConfig := buildTranscoderConfig(cfg)

	// Start pipeline (use pipeCtx, not a timeout context, because Start derives
	// the pipeline's lifetime context from the passed-in context)
	err = s.Start(pipeCtx, transcoderConfig, cfg.muxMode, cfg.autoBitRate)
	require.NoError(t, err)

	h := &testHarness{
		t:              t,
		tempDir:        tempDir,
		outputPath:     outputPath,
		FFStream:       s,
		GRPCServer:     grpcServer,
		GRPCSrv:        grpcSrv,
		Client:         c,
		pipelineCtx:    pipeCtx,
		pipelineCancel: pipeCancel,
	}

	t.Cleanup(func() {
		pipeCancel()
		grpcServer.Stop()
	})

	return h
}

// newMultiInputHarness creates an FFStream with multiple inputs at different fallback priorities.
func newMultiInputHarness(t *testing.T, inputPaths []string, opts ...harnessOption) *testHarness {
	t.Helper()

	cfg := defaultHarnessConfig()
	for _, o := range opts {
		o(&cfg)
	}

	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "output.flv")

	ctx := context.Background()
	pipeCtx, pipeCancel := context.WithCancel(ctx)

	s, err := ffstream.New(pipeCtx, ffstream.OptionInputRetryInterval(cfg.retryInterval))
	require.NoError(t, err)

	for i, path := range inputPaths {
		_, err = s.AddInput(pipeCtx, ffstream.Resource{
			URL:      path,
			Priority: uint(i),
			InputConfig: kernel.InputConfig{
				ForceRealTime: ptr(cfg.forceRealTime),
			},
		})
		require.NoError(t, err)
	}

	err = s.AddOutputTemplate(pipeCtx, ffstream.SenderTemplate{
		URLTemplate: outputPath,
		Options: avptypes.DictionaryItems{
			{Key: "f", Value: "flv"},
		},
	})
	require.NoError(t, err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	grpcServer := grpc.NewServer()
	grpcSrv := NewGRPCServer(pipeCtx, s)
	ffstream_grpc.RegisterFFStreamServer(grpcServer, grpcSrv)
	go func() { _ = grpcServer.Serve(lis) }()

	c := client.New(lis.Addr().String())

	transcoderConfig := buildTranscoderConfig(cfg)
	err = s.Start(pipeCtx, transcoderConfig, cfg.muxMode, cfg.autoBitRate)
	require.NoError(t, err)

	h := &testHarness{
		t:              t,
		tempDir:        tempDir,
		outputPath:     outputPath,
		FFStream:       s,
		GRPCServer:     grpcServer,
		GRPCSrv:        grpcSrv,
		Client:         c,
		pipelineCtx:    pipeCtx,
		pipelineCancel: pipeCancel,
	}

	t.Cleanup(func() {
		pipeCancel()
		grpcServer.Stop()
	})

	return h
}

// newHarnessNoStart creates FFStream + gRPC but does NOT call Start().
// Useful for testing error paths (e.g., multiple output templates).
func newHarnessNoStart(t *testing.T, opts ...harnessOption) *testHarness {
	t.Helper()

	cfg := defaultHarnessConfig()
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.inputPath == "" {
		shortPath, longPath := ensureSharedInputs(t)
		if cfg.forceRealTime {
			cfg.inputPath = longPath
		} else {
			cfg.inputPath = shortPath
		}
	}

	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "output.flv")

	ctx := context.Background()
	pipeCtx, pipeCancel := context.WithCancel(ctx)

	s, err := ffstream.New(pipeCtx, ffstream.OptionInputRetryInterval(cfg.retryInterval))
	require.NoError(t, err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	grpcServer := grpc.NewServer()
	grpcSrv := NewGRPCServer(pipeCtx, s)
	ffstream_grpc.RegisterFFStreamServer(grpcServer, grpcSrv)
	go func() { _ = grpcServer.Serve(lis) }()

	c := client.New(lis.Addr().String())

	h := &testHarness{
		t:              t,
		tempDir:        tempDir,
		outputPath:     outputPath,
		FFStream:       s,
		GRPCServer:     grpcServer,
		GRPCSrv:        grpcSrv,
		Client:         c,
		pipelineCtx:    pipeCtx,
		pipelineCancel: pipeCancel,
	}

	t.Cleanup(func() {
		pipeCancel()
		grpcServer.Stop()
	})

	return h
}

func buildTranscoderConfig(cfg harnessConfig) streammuxtypes.TranscoderConfig {
	videoOpts := avptypes.DictionaryItems{
		{Key: "preset", Value: "ultrafast"},
		{Key: "tune", Value: "zerolatency"},
	}
	if cfg.resolution.Width > 0 && cfg.resolution.Height > 0 {
		videoOpts = append(videoOpts, avptypes.DictionaryItem{
			Key: "s", Value: fmt.Sprintf("%dx%d", cfg.resolution.Width, cfg.resolution.Height),
		})
	}

	return streammuxtypes.TranscoderConfig{
		Output: streammuxtypes.TranscoderOutputConfig{
			VideoTrackConfigs: []streammuxtypes.OutputVideoTrackConfig{{
				InputTrackIDs:  []int{0, 1, 2, 3},
				OutputTrackIDs: []int{0},
				CodecName:      codectypes.Name(cfg.videoCodec),
				AverageBitRate: 500_000,
				CustomOptions:  videoOpts,
				Resolution:     cfg.resolution,
			}},
			AudioTrackConfigs: []streammuxtypes.OutputAudioTrackConfig{{
				InputTrackIDs:  []int{0, 1, 2, 3},
				OutputTrackIDs: []int{1},
				CodecName:      codectypes.Name(cfg.audioCodec),
				SampleRate:     cfg.sampleRate,
				CustomOptions: avptypes.DictionaryItems{
					{Key: "ar", Value: fmt.Sprintf("%d", cfg.sampleRate)},
					{Key: "ac", Value: "1"},
				},
			}},
		},
	}
}

// waitForDataFlow polls GetStats until at least one packet has been processed.
func (h *testHarness) waitForDataFlow(timeout time.Duration) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.pipelineCtx, timeout)
	defer cancel()
	for {
		stats, err := h.Client.GetStats(ctx)
		if err == nil {
			videoCount := stats.GetNodeCounters().GetProcessed().GetPackets().GetVideo().GetCount()
			audioCount := stats.GetNodeCounters().GetProcessed().GetPackets().GetAudio().GetCount()
			if videoCount+audioCount > 0 {
				h.t.Logf("pipeline active: processed video=%d audio=%d packets", videoCount, audioCount)
				return
			}
		}
		select {
		case <-ctx.Done():
			h.t.Fatalf("timeout (%v) waiting for data to flow through pipeline", timeout)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// waitForCompletion waits for the pipeline to naturally complete (input EOF).
func (h *testHarness) waitForCompletion(timeout time.Duration) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := h.FFStream.Wait(ctx)
	if err != nil && ctx.Err() == nil {
		h.t.Logf("pipeline wait returned: %v (may be expected)", err)
	}
}

// --- Output verification ---

type ffprobeStream struct {
	Index      int    `json:"index"`
	CodecName  string `json:"codec_name"`
	CodecType  string `json:"codec_type"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	SampleRate string `json:"sample_rate,omitempty"`
}

type ffprobeFormat struct {
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	Size       string `json:"size"`
}

type ffprobeResult struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

func verifyOutputFile(t *testing.T, path string) *ffprobeResult {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err, "output file should exist")
	require.Greater(t, info.Size(), int64(0), "output file should not be empty")

	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams", "-show_format",
		path,
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ffprobe failed: %s", out)

	var result ffprobeResult
	require.NoError(t, json.Unmarshal(out, &result), "ffprobe output is not valid JSON")

	return &result
}

func (r *ffprobeResult) hasStreamType(codecType string) bool {
	for _, s := range r.Streams {
		if s.CodecType == codecType {
			return true
		}
	}
	return false
}

func (r *ffprobeResult) streamByType(codecType string) *ffprobeStream {
	for i := range r.Streams {
		if r.Streams[i].CodecType == codecType {
			return &r.Streams[i]
		}
	}
	return nil
}
