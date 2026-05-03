//go:build test_e2e && test_local_ffstream

package e2e

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	child_process_manager "github.com/AgustinSRG/go-child-process-manager"
	"github.com/dustin/go-humanize"
	"github.com/facebookincubator/go-belt/tool/logger"
	xlogrus "github.com/facebookincubator/go-belt/tool/logger/implementation/logrus"
	"github.com/sirupsen/logrus"
	audio "github.com/xaionaro-go/audio/pkg/audio/types"
	"github.com/xaionaro-go/avd/pkg/avd"
	"github.com/xaionaro-go/avd/pkg/config"
	"github.com/xaionaro-go/avd/pkg/configapplier"
	"github.com/xaionaro-go/avd/pkg/configfile"
	"github.com/xaionaro-go/avpipeline/codec"
	codectypes "github.com/xaionaro-go/avpipeline/codec/types"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	ffcert "github.com/xaionaro-go/ffstream/pkg/cert"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/client"
)

const (
	monitorDuration      = 2 * time.Minute
	monitorInterval      = 5 * time.Second
	maxConsecutiveZero   = 4
	activeBitrateBps     = 100_000.0
	nearZeroBitrateBps   = 1_000.0
	startupTimeout       = 90 * time.Second
	shutdownGraceTimeout = 10 * time.Second
	avdDefaultConfigPath = "~/.avd.conf:/etc/avd/avd.conf"
)

type localPorts struct {
	avdPublishPort      int
	avdConsumePort      int
	ffstreamControlPort int
}

type localGoService struct {
	cancel context.CancelFunc
	done   chan struct{}

	errLocker sync.RWMutex
	err       error
}

func (s *localGoService) setErr(err error) {
	s.errLocker.Lock()
	defer s.errLocker.Unlock()
	s.err = err
}

func (s *localGoService) Error() error {
	s.errLocker.RLock()
	defer s.errLocker.RUnlock()
	return s.err
}

func TestLocalFFstreamBitrate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	if _, err := os.Stat("/tmp/input0-3.flv"); err != nil {
		t.Skipf("missing fallback input file /tmp/input0-3.flv: %v", err)
	}

	oldCUDAVisibleDevices, hadCUDAVisibleDevices := os.LookupEnv("CUDA_VISIBLE_DEVICES")
	if err := os.Setenv("CUDA_VISIBLE_DEVICES", "1"); err != nil {
		t.Fatalf("unable to set CUDA_VISIBLE_DEVICES: %v", err)
	}
	t.Cleanup(func() {
		if hadCUDAVisibleDevices {
			_ = os.Setenv("CUDA_VISIBLE_DEVICES", oldCUDAVisibleDevices)
			return
		}
		_ = os.Unsetenv("CUDA_VISIBLE_DEVICES")
	})

	ports := allocateLocalPorts(t)
	tempDir, err := os.MkdirTemp("", "local-ffstream-bitrate-")
	if err != nil {
		t.Fatalf("unable to create temp dir: %v", err)
	}

	avdSvc := startLocalAVD(t, ctx, tempDir, ports)
	var ffstreamSvc *localGoService

	t.Cleanup(func() {
		stopLocalService(t, ffstreamSvc)
		stopLocalService(t, avdSvc)
		if t.Failed() {
			t.Logf("debug logs: %s", tempDir)
			return
		}
		_ = os.RemoveAll(tempDir)
	})

	waitForPort(t, ctx, ports.avdPublishPort, startupTimeout)
	ffstreamSvc = startLocalFFstream(t, ctx, tempDir, ports)
	waitForPort(t, ctx, ports.ffstreamControlPort, startupTimeout)

	ctl := client.New(fmt.Sprintf("tcp+ssl:127.0.0.1:%d", ports.ffstreamControlPort))
	deadline := time.Now().Add(monitorDuration)

	hadActiveBitrate := false
	consecutiveZero := 0
	consecutiveQueryErrors := 0

	for time.Now().Before(deadline) {
		if !isServiceAlive(ffstreamSvc) {
			failWithLogs(t, tempDir, fmt.Sprintf("ffstream service exited unexpectedly: %v", ffstreamSvc.Error()))
		}

		bitRates, err := ctl.GetBitRates(ctx)
		if err != nil {
			consecutiveQueryErrors++
			if consecutiveQueryErrors >= 3 {
				failWithLogs(t, tempDir, fmt.Sprintf("GetBitRates failed %d times: %v", consecutiveQueryErrors, err))
			}
			t.Logf("GetBitRates transient error (%d): %v", consecutiveQueryErrors, err)
			time.Sleep(monitorInterval)
			continue
		}
		consecutiveQueryErrors = 0

		total := totalBitrate(bitRates)
		t.Logf("bitrates: total=%.0f input(v=%.0f,a=%.0f) encoded(v=%.0f,a=%.0f) output(v=%.0f,a=%.0f)",
			total,
			float64(bitRates.Input.Video), float64(bitRates.Input.Audio),
			float64(bitRates.Encoded.Video), float64(bitRates.Encoded.Audio),
			float64(bitRates.Output.Video), float64(bitRates.Output.Audio),
		)

		if total > activeBitrateBps {
			hadActiveBitrate = true
			consecutiveZero = 0
		} else if hadActiveBitrate && total <= nearZeroBitrateBps {
			consecutiveZero++
			if consecutiveZero >= maxConsecutiveZero {
				failWithLogs(t, tempDir, fmt.Sprintf("bitrate collapsed to near-zero for %d consecutive samples", consecutiveZero))
			}
		} else {
			consecutiveZero = 0
		}

		time.Sleep(monitorInterval)
	}

	if !hadActiveBitrate {
		failWithLogs(t, tempDir, "stream never reached active bitrate")
	}
}

func startLocalAVD(t *testing.T, parentCtx context.Context, tempDir string, ports localPorts) *localGoService {
	t.Helper()
	logPath := filepath.Join(tempDir, "avd.log")
	return startLocalGoService(t, parentCtx, logPath, func(ctx context.Context, logOutput io.Writer) error {
		return runLocalAVD(ctx, logOutput, ports)
	})
}

func runLocalAVD(ctx context.Context, logOutput io.Writer, ports localPorts) error {
	ctx = withTestLogger(ctx, logger.LevelWarning, logOutput)

	cfg, err := loadLocalAVDConfig(ctx, ports)
	if err != nil {
		return err
	}

	srv := avd.NewServer(ctx)
	if err := configapplier.ApplyConfig(ctx, cfg, srv); err != nil {
		return fmt.Errorf("unable to apply AVD config: %w", err)
	}

	go func() {
		<-ctx.Done()
		_ = srv.Close(context.Background())
	}()

	err = srv.Wait(ctx)
	if isIgnorableServiceErr(err, ctx.Err()) {
		return nil
	}
	if err == nil && ctx.Err() == nil {
		return fmt.Errorf("avd exited unexpectedly")
	}
	return err
}

func loadLocalAVDConfig(ctx context.Context, ports localPorts) (config.Config, error) {
	info := map[string]any{
		"env": environWithOverrides(map[string]string{
			"AVD_PORT_PUBLISHERS": strconv.Itoa(ports.avdPublishPort),
			"AVD_PORT_CONSUMERS":  strconv.Itoa(ports.avdConsumePort),
		}),
	}

	var cfg config.Config
	for _, cfgPath := range strings.Split(avdDefaultConfigPath, ":") {
		exists, err := configfile.Read(ctx, cfgPath, &cfg, info)
		if err != nil {
			return config.Config{}, fmt.Errorf("unable to read AVD config %q: %w", cfgPath, err)
		}
		if exists {
			return cfg, nil
		}
	}
	return cfg, nil
}

func environWithOverrides(overrides map[string]string) map[string]string {
	envs := map[string]string{}
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		envs[parts[0]] = parts[1]
	}
	for k, v := range overrides {
		envs[k] = v
	}
	return envs
}

func startLocalFFstream(t *testing.T, parentCtx context.Context, tempDir string, ports localPorts) *localGoService {
	t.Helper()
	logPath := filepath.Join(tempDir, "ffstream.log")
	return startLocalGoService(t, parentCtx, logPath, func(ctx context.Context, logOutput io.Writer) error {
		return runLocalFFstream(ctx, logOutput, ports)
	})
}

func runLocalFFstream(ctx context.Context, logOutput io.Writer, ports localPorts) error {
	ctx = withTestLogger(ctx, logger.LevelWarning, logOutput)

	if err := child_process_manager.InitializeChildProcessManager(); err != nil {
		return fmt.Errorf("unable to initialize child process manager: %w", err)
	}
	defer child_process_manager.DisposeChildProcessManager()

	s, err := ffstream.New(ctx,
		ffstream.OptionInputRetryIntervalValue(time.Second),
	)
	if err != nil {
		return fmt.Errorf("unable to initialize ffstream: %w", err)
	}

	controlListener, err := newTLSControlListener(fmt.Sprintf("127.0.0.1:%d", ports.ffstreamControlPort))
	if err != nil {
		return fmt.Errorf("unable to create control listener: %w", err)
	}
	defer controlListener.Close()

	controlErrCh := make(chan error, 1)
	go func() {
		controlErrCh <- ffstreamserver.New(s).ServeContext(ctx, controlListener)
	}()

	if _, err := s.AddInput(ctx, ffstream.Resource{
		URL:          "rtmp://127.0.0.1:1935/proxy/dji-osmo-pocket3",
		CodecHWAccel: avptypes.HardwareDeviceTypeNone,
		InputConfig: kernel.InputConfig{
			ForceRealTime: ptr(false),
			CustomOptions: avptypes.DictionaryItems{
				{Key: "fflags", Value: "nobuffer"},
				{Key: "flags", Value: "low_delay"},
				{Key: "rtbufsize", Value: "5M"},
				{Key: "probesize", Value: "32768"},
				{Key: "analyzeduration", Value: "200000"},
				{Key: "video_size", Value: "1920x1080"},
			},
		},
	}); err != nil {
		return fmt.Errorf("unable to add primary input: %w", err)
	}

	if _, err := s.AddInput(ctx, ffstream.Resource{
		URL:          "/tmp/input0-3.flv",
		Priority:     1,
		CodecHWAccel: avptypes.HardwareDeviceTypeNone,
		InputConfig: kernel.InputConfig{
			ForceRealTime: ptr(false),
		},
	}); err != nil {
		return fmt.Errorf("unable to add fallback input: %w", err)
	}

	outputURL := fmt.Sprintf("rtmp://127.0.0.1:%d/pixel/dji-osmo-pocket-3-${v:0:codec}${a:0:codec}-${v:0:height}${a:0:rate}/", ports.avdPublishPort)
	if err := s.AddOutputTemplate(ctx, ffstream.SenderTemplate{
		URLTemplate: outputURL,
		Options: avptypes.DictionaryItems{
			{Key: "f", Value: "flv"},
		},
	}); err != nil {
		return fmt.Errorf("unable to add output template: %w", err)
	}

	transcoderConfig, autoBitRateConfig, err := buildLocalFFstreamTranscoderConfig(ctx)
	if err != nil {
		return err
	}

	if err := s.Start(
		ctx,
		transcoderConfig,
		streammuxtypes.MuxModeDifferentOutputsSameTracksSplitAV,
		autoBitRateConfig,
	); err != nil {
		return fmt.Errorf("unable to start ffstream: %w", err)
	}

	waitErrCh := make(chan error, 1)
	go func() {
		waitErrCh <- s.Wait(ctx)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-controlErrCh:
			if isIgnorableServiceErr(err, ctx.Err()) {
				controlErrCh = nil
				continue
			}
			if err == nil {
				return fmt.Errorf("control server exited unexpectedly")
			}
			return fmt.Errorf("control server exited: %w", err)
		case err := <-waitErrCh:
			if isIgnorableServiceErr(err, ctx.Err()) {
				return nil
			}
			if err == nil {
				return fmt.Errorf("ffstream exited unexpectedly")
			}
			return fmt.Errorf("ffstream exited: %w", err)
		}
	}
}

func buildLocalFFstreamTranscoderConfig(
	ctx context.Context,
) (streammuxtypes.TranscoderConfig, *streammuxtypes.AutoBitRateVideoConfig, error) {
	videoCodec := codec.Name("hevc_nvenc")
	videoCodecValue := videoCodec.Codec(ctx, true)
	if videoCodecValue == nil {
		return streammuxtypes.TranscoderConfig{}, nil, fmt.Errorf("unable to resolve video codec %q", videoCodec)
	}

	videoBitrate, err := humanize.ParseBytes("5M")
	if err != nil {
		return streammuxtypes.TranscoderConfig{}, nil, fmt.Errorf("unable to parse video bitrate: %w", err)
	}

	var videoOptions avptypes.DictionaryItems
	videoOptions = append(videoOptions, codec.LowLatencyOptions(ctx, videoCodec, true)...)
	videoOptions = append(videoOptions, avptypes.DictionaryItem{Key: "s", Value: "1920x1080"})
	videoOptions = videoOptions.Deduplicate()

	audioOptions := avptypes.DictionaryItems{
		{Key: "ar", Value: "48000"},
		{Key: "ac", Value: "1"},
	}.Deduplicate()

	trackIDs := []int{0, 1, 2, 3, 4, 5, 6, 7}
	transcoderConfig := streammuxtypes.TranscoderConfig{
		Output: streammuxtypes.TranscoderOutputConfig{
			VideoTrackConfigs: []streammuxtypes.OutputVideoTrackConfig{{
				InputTrackIDs:  trackIDs,
				OutputTrackIDs: []int{0},
				CodecName:      codectypes.Name(videoCodec),
				AverageBitRate: uint64(videoBitrate),
				CustomOptions:  videoOptions,
				Resolution: codec.Resolution{
					Width:  1920,
					Height: 1080,
				},
			}},
			AudioTrackConfigs: []streammuxtypes.OutputAudioTrackConfig{{
				InputTrackIDs:  trackIDs,
				OutputTrackIDs: []int{1},
				CodecName:      codectypes.Name("aac"),
				CustomOptions:  audioOptions,
				SampleRate:     audio.SampleRate(48000),
			}},
		},
	}

	autoBitRateConfig, err := streammux.DefaultAutoBitRateVideoConfig(videoCodecValue.ID())
	if err != nil {
		return streammuxtypes.TranscoderConfig{}, nil, fmt.Errorf("unable to get default auto bitrate config: %w", err)
	}
	autoBitRateConfig.ResolutionsAndBitRates = autoBitRateConfig.ResolutionsAndBitRates.MaxHeight(uint32(1080))
	autoBitRateConfig.ResolutionsAndBitRates = autoBitRateConfig.ResolutionsAndBitRates.MinHeight(uint32(180))
	autoBitRateConfig.AutoByPass = false
	autoBitRateConfig.MaxBitRate = autoBitRateConfig.ResolutionsAndBitRates.Best().BitrateHigh
	autoBitRateConfig.MinBitRate = autoBitRateConfig.ResolutionsAndBitRates.Worst().BitrateLow

	return transcoderConfig, &autoBitRateConfig, nil
}

func newTLSControlListener(addr string) (net.Listener, error) {
	serverCert, err := ffcert.GenerateSelfSignedForServer()
	if err != nil {
		return nil, fmt.Errorf("failed to generate self-signed certificate: %w", err)
	}

	listener, err := tls.Listen("tcp", addr, &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		NextProtos:   []string{"h2"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create TLS listener at %s: %w", addr, err)
	}
	return listener, nil
}

func withTestLogger(ctx context.Context, level logger.Level, output io.Writer) context.Context {
	logrusLogger := logrus.New()
	logrusLogger.SetOutput(output)
	logrusLogger.SetFormatter(&logrus.TextFormatter{
		DisableColors: true,
		FullTimestamp: true,
	})
	logrusLogger.SetLevel(logrus.WarnLevel)

	l := xlogrus.New(logrusLogger).WithLevel(level)
	return logger.CtxWithLogger(ctx, l)
}

func startLocalGoService(
	t *testing.T,
	parentCtx context.Context,
	logPath string,
	runFn func(context.Context, io.Writer) error,
) *localGoService {
	t.Helper()

	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("unable to create log file %s: %v", logPath, err)
	}

	ctx, cancel := context.WithCancel(parentCtx)
	svc := &localGoService{
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go func() {
		defer close(svc.done)
		defer logFile.Close()

		runErr := runFn(ctx, logFile)
		if isIgnorableServiceErr(runErr, ctx.Err()) {
			runErr = nil
		}
		if runErr != nil {
			_, _ = fmt.Fprintf(logFile, "\nservice exited with error: %v\n", runErr)
		}
		svc.setErr(runErr)
	}()

	return svc
}

func stopLocalService(t *testing.T, svc *localGoService) {
	t.Helper()
	if svc == nil {
		return
	}

	svc.cancel()
	select {
	case <-svc.done:
	case <-time.After(shutdownGraceTimeout):
		t.Logf("timed out waiting for local service shutdown")
	}
}

func isServiceAlive(svc *localGoService) bool {
	if svc == nil {
		return false
	}
	select {
	case <-svc.done:
		return false
	default:
		return true
	}
}

func isIgnorableServiceErr(err error, ctxErr error) bool {
	if err == nil {
		return ctxErr != nil
	}
	if ctxErr != nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}

func waitForPort(t *testing.T, ctx context.Context, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			t.Fatalf("context ended while waiting for %s: %v", addr, ctx.Err())
		default:
		}

		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}

	t.Fatalf("timeout waiting for %s", addr)
}

func allocateLocalPorts(t *testing.T) localPorts {
	t.Helper()
	return localPorts{
		avdPublishPort:      mustReservePort(t),
		avdConsumePort:      mustReservePort(t),
		ffstreamControlPort: mustReservePort(t),
	}
}

func mustReservePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unable to reserve a local port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func totalBitrate(br *streammuxtypes.BitRates) float64 {
	if br == nil {
		return 0
	}
	return float64(br.Input.Video + br.Input.Audio + br.Input.Other +
		br.Encoded.Video + br.Encoded.Audio + br.Encoded.Other +
		br.Output.Video + br.Output.Audio + br.Output.Other)
}

func failWithLogs(t *testing.T, tempDir, msg string) {
	t.Helper()
	ffstreamLogPath := filepath.Join(tempDir, "ffstream.log")
	avdLogPath := filepath.Join(tempDir, "avd.log")
	ffstreamTail := readLogTail(ffstreamLogPath, 120)
	avdTail := readLogTail(avdLogPath, 120)
	t.Fatalf("%s\nffstream.log tail (%s):\n%s\navd.log tail (%s):\n%s", msg, ffstreamLogPath, ffstreamTail, avdLogPath, avdTail)
}

func readLogTail(path string, maxLines int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("<unable to read %s: %v>", path, err)
	}
	lines := splitLines(string(b))
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return joinLines(lines)
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	out := lines[0]
	for i := 1; i < len(lines); i++ {
		out += "\n" + lines[i]
	}
	return out
}

func ptr[T any](v T) *T {
	return &v
}
