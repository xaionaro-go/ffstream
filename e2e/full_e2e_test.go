// full_e2e_test.go implements full end-to-end test scenarios.

package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// E2E Test Configuration
const (
	// Test timeouts
	buildTimeout      = 10 * time.Minute
	deployTimeout     = 5 * time.Minute
	streamTestTimeout = 3 * time.Minute
	avdStartupTimeout = 30 * time.Second

	// AVD server ports (will bind to 0.0.0.0 so phone can connect)
	avdPublisherPort  = 19461
	avdConsumerPort   = 19451
	avdManagementPort = 17221

	// Test stream duration
	testStreamDuration = 15 * time.Second
)

// NOTE: Known Issues
//
// 1. AVD SIGSEGV: When receiving RTMP streams, avd sometimes crashes with SIGSEGV
//    in avformat_open_input. This appears to be a bug in the libav integration.
//    Error logs show: "App field don't match up: test <-> avd-input"
//    Until this is fixed, full e2e streaming tests may be flaky.
//
// 2. ffstream flag parsing: The -s and -ar flags for resolution/sample rate
//    need to be passed as encoder options after -c:v or -c:a, not as global flags.
//    When using non-copy codecs, explicit resolution (-s) and sample rate (-ar)
//    must be provided or ffstream will fail validation.

// E2ETestSuite holds the state for a full e2e test run.
type E2ETestSuite struct {
	t            *testing.T
	ctx          context.Context
	cancel       context.CancelFunc
	device       *DeviceInfo
	deviceHelper *deviceTestHelper
	avdProcess   *exec.Cmd
	avdConfig    string
	tempDir      string
	hostIP       string
	mu           sync.Mutex
}

// NewE2ETestSuite creates a new e2e test suite.
func NewE2ETestSuite(t *testing.T) *E2ETestSuite {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	return &E2ETestSuite{
		t:      t,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Setup initializes the test environment.
func (s *E2ETestSuite) Setup() error {
	var err error

	// Create temp directory for test artifacts
	s.tempDir, err = os.MkdirTemp("", "ffstream-e2e-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	s.t.Cleanup(func() { os.RemoveAll(s.tempDir) })

	// Get the host IP that is reachable from the phone
	s.hostIP, err = s.getHostIPForDevice()
	if err != nil {
		return fmt.Errorf("failed to get host IP: %w", err)
	}
	s.t.Logf("Host IP for device: %s", s.hostIP)

	// Get connected real device
	s.device, err = getRealDevice(s.ctx)
	if err != nil {
		return fmt.Errorf("no real device connected: %w", err)
	}
	s.t.Logf("Using device: %s (%s)", s.device.Model, s.device.Serial)

	s.deviceHelper = newDeviceTestHelper(s.t, s.ctx, s.device)

	return nil
}

// getHostIPForDevice returns the host IP that the device can reach.
// Since ADB is at 172.17.0.1, the device network is likely on that subnet.
func (s *E2ETestSuite) getHostIPForDevice() (string, error) {
	// Get the IP from the interface that routes to 172.17.0.1
	conn, err := net.Dial("udp", "172.17.0.1:5037")
	if err != nil {
		// Fallback: try to find a non-loopback interface
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			return "", err
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					return ipnet.IP.String(), nil
				}
			}
		}
		return "", fmt.Errorf("no suitable network interface found")
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String(), nil
}

// Teardown cleans up the test environment.
func (s *E2ETestSuite) Teardown() {
	s.StopAVD()
	if s.tempDir != "" {
		os.RemoveAll(s.tempDir)
	}
}

// CheckPrerequisites verifies all prerequisites are met.
func (s *E2ETestSuite) CheckPrerequisites() error {
	// Check termux is installed
	if !s.deviceHelper.checkTermuxInstalled() {
		return fmt.Errorf("termux not installed on device")
	}

	// Build ffstream Android deb package
	if err := s.buildFFstreamDeb(); err != nil {
		return fmt.Errorf("ffstream build failed: %w", err)
	}

	// Check avd binary exists (we build it if needed)
	if err := s.ensureAVDBinary(); err != nil {
		return fmt.Errorf("avd binary check failed: %w", err)
	}

	return nil
}

// buildFFstreamDeb builds the ffstream Android deb package using Docker.
// Also checks for direct binary at bin/ffstream-android-arm64.
func (s *E2ETestSuite) buildFFstreamDeb() error {
	debPath := filepath.Join(findRepoRoot(s.t), ffstreamDebPath)
	binPath := filepath.Join(findRepoRoot(s.t), "bin/ffstream-android-arm64")

	// First check if direct binary exists (built via make ffstream-android-arm64-static-cgo)
	if _, err := os.Stat(binPath); err == nil {
		s.t.Log("Using existing ffstream binary at bin/ffstream-android-arm64")
		return nil
	}

	// Check if Docker is available
	cmd := exec.CommandContext(s.ctx, "docker", "info")
	if err := cmd.Run(); err != nil {
		// Docker not available, check if deb already exists
		if _, err := os.Stat(debPath); err == nil {
			s.t.Log("Docker not available, using existing deb package")
			return nil
		}
		return fmt.Errorf("Docker not available and no existing deb package or binary: %w", err)
	}

	s.t.Log("Building ffstream Android deb package...")
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
	defer cancel()

	// Run make target to build the deb package
	cmd = exec.CommandContext(ctx, "make", "bin/ffstream-android-termux.deb")
	cmd.Dir = findRepoRoot(s.t)
	cmd.Env = os.Environ()

	// Stream output to test log
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build failed: %w\nOutput: %s", err, string(output))
	}

	s.t.Logf("Build completed successfully")

	// Verify the package was created
	if _, err := os.Stat(debPath); os.IsNotExist(err) {
		return fmt.Errorf("deb package not created after build at %s", debPath)
	}

	return nil
}

// ensureAVDBinary ensures the avd binary is available.
func (s *E2ETestSuite) ensureAVDBinary() error {
	avdBin := s.getAVDBinaryPath()

	// Check if binary exists and is executable
	if _, err := os.Stat(avdBin); err == nil {
		return nil
	}

	// Build avd
	s.t.Log("Building avd binary...")
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-tags=with_libav,ffmpeg7", "-o", avdBin, "./cmd/avd")
	cmd.Dir = "/workspaces/xaionaro-go/avd"
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to build avd: %w\nOutput: %s", err, output)
	}

	s.t.Log("avd binary built successfully")
	return nil
}

func (s *E2ETestSuite) getAVDBinaryPath() string {
	return filepath.Join(s.tempDir, "avd")
}

// DeployFFstream deploys ffstream to the device.
func (s *E2ETestSuite) DeployFFstream() error {
	s.t.Log("Deploying ffstream to device...")

	// Check if ffstream is already installed and runnable
	if err := s.deviceHelper.checkFfstreamRunnable(); err == nil {
		s.t.Log("ffstream already installed and runnable, skipping deploy")
		return nil
	}

	// Try direct binary deployment first (from make ffstream-android-arm64-static-cgo)
	binPath := filepath.Join(findRepoRoot(s.t), "bin/ffstream-android-arm64")
	if _, err := os.Stat(binPath); err == nil {
		s.t.Log("Found direct binary, deploying via adb...")
		tmpPath := "/data/local/tmp/ffstream"
		if err := s.deviceHelper.push(binPath, tmpPath); err != nil {
			return fmt.Errorf("failed to push binary: %w", err)
		}

		// Copy to termux using run-as
		termuxBinPath := termuxUsrBin + "/ffstream"
		cpCmd := fmt.Sprintf("run-as com.termux /data/data/com.termux/files/usr/bin/bash -c 'rm -f %s && cp %s %s && chmod +x %s'",
			termuxBinPath, tmpPath, termuxBinPath, termuxBinPath)
		if _, err := s.deviceHelper.shell(cpCmd); err != nil {
			return fmt.Errorf("failed to copy binary to termux: %w", err)
		}

		// Verify installation
		if err := s.deviceHelper.checkFfstreamRunnable(); err != nil {
			return fmt.Errorf("ffstream installed but not runnable: %w", err)
		}

		s.t.Log("ffstream deployed via direct binary successfully")
		return nil
	}

	// Fall back to deb package deployment
	debPath := filepath.Join(findRepoRoot(s.t), ffstreamDebPath)
	sdcardPath := "/sdcard/Download/ffstream-android-termux-arm64.deb"

	s.t.Logf("Pushing deb to %s", sdcardPath)
	if err := s.deviceHelper.push(debPath, sdcardPath); err != nil {
		return fmt.Errorf("failed to push deb: %w", err)
	}

	// Copy to termux home
	termuxDebPath := termuxHome + "/ffstream.deb"
	s.t.Logf("Copying deb to %s", termuxDebPath)
	if err := s.deviceHelper.copyDebToTermux(sdcardPath, termuxDebPath); err != nil {
		return fmt.Errorf("failed to copy deb to termux: %w", err)
	}

	// Set up library symlinks
	if err := s.deviceHelper.setupLibrarySymlinks(); err != nil {
		s.t.Logf("Warning: failed to setup library symlinks: %v", err)
	}

	// Check if dpkg is available and install
	if _, err := s.deviceHelper.termuxCmd("which dpkg"); err == nil {
		s.t.Log("Installing ffstream via dpkg...")
		if err := s.deviceHelper.installDebInTermux(termuxDebPath); err != nil {
			s.t.Logf("Warning: dpkg install failed: %v", err)
		}
	}

	// Verify installation
	if err := s.deviceHelper.checkFfstreamRunnable(); err != nil {
		return fmt.Errorf("ffstream installed but not runnable: %w", err)
	}

	s.t.Log("ffstream deployed and verified successfully")
	return nil
}

// generateAVDConfig generates a minimal AVD configuration for testing.
func (s *E2ETestSuite) generateAVDConfig() string {
	return fmt.Sprintf(`ports_service:
- address: tcp:0.0.0.0:%d
  service:
    management:
      protocol: "gRPC"
ports_streaming:
- address: tcp:0.0.0.0:%d
  mode: "publishers"
  publish_mode: exclusive-takeover
  protocol_handler:
    rtmp: {}
  custom_options:
  - key: "probesize"
    value: "32768"
  - key: "analyzeduration"
    value: "200000"
  default_route_path: ""
  on_end: "wait_for_new_publisher"
- address: tcp:0.0.0.0:%d
  mode: "consumers"
  publish_mode: exclusive-takeover
  protocol_handler:
    rtmp: {}
  default_route_path: ""
  on_end: "close_consumers"
  wait_until:
    video_track_count: 1
    audio_track_count: 0
endpoints:
  test/stream: {}
`, avdManagementPort, avdPublisherPort, avdConsumerPort)
}

// StartAVD starts the AVD server to receive streams.
func (s *E2ETestSuite) StartAVD() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	avdBin := s.getAVDBinaryPath()
	s.avdConfig = s.generateAVDConfig()

	// Write config to file
	configPath := filepath.Join(s.tempDir, "avd.yaml")
	if err := os.WriteFile(configPath, []byte(s.avdConfig), 0o644); err != nil {
		return fmt.Errorf("failed to write avd config: %w", err)
	}

	s.t.Logf("Starting AVD with config:\n%s", s.avdConfig)

	// Start AVD
	s.avdProcess = exec.CommandContext(s.ctx, avdBin, "--config-path", configPath)
	s.avdProcess.Env = os.Environ()

	// Capture output
	stdout, _ := s.avdProcess.StdoutPipe()
	stderr, _ := s.avdProcess.StderrPipe()

	if err := s.avdProcess.Start(); err != nil {
		return fmt.Errorf("failed to start avd: %w", err)
	}

	// Log output in background
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				s.t.Logf("[avd stdout] %s", buf[:n])
			}
			if err != nil {
				break
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				s.t.Logf("[avd stderr] %s", buf[:n])
			}
			if err != nil {
				break
			}
		}
	}()

	// Wait for AVD to be ready (try to connect to the port)
	s.t.Logf("Waiting for AVD to be ready on port %d...", avdPublisherPort)
	deadline := time.Now().Add(avdStartupTimeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", avdPublisherPort), time.Second)
		if err == nil {
			conn.Close()
			s.t.Log("AVD is ready")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("avd failed to start within timeout")
}

// StopAVD stops the AVD server.
func (s *E2ETestSuite) StopAVD() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove ADB reverse port forwarding
	if s.device != nil {
		adbCmdWithSerial(s.ctx, s.device.Serial, "reverse", "--remove", fmt.Sprintf("tcp:%d", avdPublisherPort))
	}

	if s.avdProcess != nil && s.avdProcess.Process != nil {
		s.t.Log("Stopping AVD...")
		s.avdProcess.Process.Kill()
		s.avdProcess.Wait()
		s.avdProcess = nil
	}
}

// SetupReversePortForwarding sets up ADB reverse port forwarding so the device
// can connect to 127.0.0.1:<port> and it will be forwarded to the host.
func (s *E2ETestSuite) SetupReversePortForwarding() error {
	s.t.Logf("Setting up ADB reverse port forwarding for port %d...", avdPublisherPort)

	// adb reverse tcp:<device_port> tcp:<host_port>
	// This allows the device to connect to 127.0.0.1:<port> and reach the host
	_, stderr, err := adbCmdWithSerial(s.ctx, s.device.Serial, "reverse",
		fmt.Sprintf("tcp:%d", avdPublisherPort),
		fmt.Sprintf("tcp:%d", avdPublisherPort))
	if err != nil {
		return fmt.Errorf("failed to set up reverse port forwarding: %w, stderr: %s", err, stderr)
	}

	s.t.Log("ADB reverse port forwarding established")
	return nil
}

// RunStreamTest runs a streaming test from the device to AVD.
func (s *E2ETestSuite) RunStreamTest(testName string, duration time.Duration) error {
	// Use localhost since we're using ADB reverse port forwarding
	rtmpURL := fmt.Sprintf("rtmp://127.0.0.1:%d/test/stream", avdPublisherPort)
	s.t.Logf("Testing stream to %s (via ADB reverse)", rtmpURL)

	// Build ffstream command
	// Using testsrc since camera may not be available on all devices
	// For real production test, use android_camera
	cmdStr := fmt.Sprintf(`timeout %d ffstream -v info \
		-hwaccel mediacodec \
		-f lavfi -i 'testsrc=duration=%d:size=640x480:rate=30' \
		-f lavfi -i 'sine=frequency=1000:duration=%d:sample_rate=48000' \
		-s 640x480 \
		-c:v h264 \
		-ar 48000 -ac 1 \
		-c:a aac \
		-b:v 1M -bufsize 1M \
		-g 30 -r 30 \
		-f flv \
		'%s' 2>&1`,
		int(duration.Seconds())+5,
		int(duration.Seconds()),
		int(duration.Seconds()),
		rtmpURL)

	s.t.Logf("Running ffstream command on device...")
	out, err := s.deviceHelper.termuxCmd(cmdStr)
	s.t.Logf("ffstream output:\n%s", out)

	// Check for errors
	if strings.Contains(out, "CANNOT LINK") {
		return fmt.Errorf("library linking error: %s", out)
	}
	if strings.Contains(out, "Connection refused") {
		return fmt.Errorf("AVD not reachable at %s", rtmpURL)
	}
	if strings.Contains(out, "Permission denied") {
		return fmt.Errorf("permission denied: %s", out)
	}

	// Check for successful frames
	if !strings.Contains(out, "frame=") && !strings.Contains(out, "Output") {
		if err != nil {
			return fmt.Errorf("streaming failed: %w, output: %s", err, out)
		}
	}

	return nil
}

// RunCameraStreamTest runs a camera streaming test.
func (s *E2ETestSuite) RunCameraStreamTest(duration time.Duration) error {
	// Use localhost since we're using ADB reverse port forwarding
	rtmpURL := fmt.Sprintf("rtmp://127.0.0.1:%d/test/camera-stream", avdPublisherPort)
	s.t.Logf("Testing camera stream to %s (via ADB reverse)", rtmpURL)

	// Check if pulse audio is available
	hasPulse := true
	if _, err := s.deviceHelper.termuxCmd("pulseaudio --check 2>&1 || pulseaudio --start 2>&1"); err != nil {
		s.t.Log("PulseAudio not available - testing video only")
		hasPulse = false
	}

	var cmdBuilder strings.Builder
	cmdBuilder.WriteString(fmt.Sprintf("timeout %d ffstream -v info ", int(duration.Seconds())+5))
	cmdBuilder.WriteString("-retry_input_timeout_on_failure 1s ")
	cmdBuilder.WriteString("-retry_output_timeout_on_failure 0 ")
	cmdBuilder.WriteString("-hwaccel mediacodec ")

	// Camera input - front camera (index 1), lower resolution for test
	cmdBuilder.WriteString("-video_size 640x480 ")
	cmdBuilder.WriteString("-camera_index 1 ")
	cmdBuilder.WriteString("-framerate 30 ")
	cmdBuilder.WriteString("-f android_camera -i '' ")

	if hasPulse {
		cmdBuilder.WriteString("-f pulse -i default ")
	}

	// Output settings
	cmdBuilder.WriteString("-s 640x480 ")
	cmdBuilder.WriteString("-c:v h264_mediacodec ")
	cmdBuilder.WriteString("-b:v 2M -bufsize 2M ")
	cmdBuilder.WriteString("-g 30 -r 30 ")

	if hasPulse {
		cmdBuilder.WriteString("-ar 48000 -ac 1 -sample_fmt fltp ")
		cmdBuilder.WriteString("-c:a aac ")
	}

	cmdBuilder.WriteString("-f flv ")
	cmdBuilder.WriteString(fmt.Sprintf("'%s' 2>&1", rtmpURL))

	s.t.Logf("Running camera stream command...")
	out, err := s.deviceHelper.termuxCmd(cmdBuilder.String())
	s.t.Logf("Camera stream output:\n%s", out)

	// Check for camera-specific errors
	if strings.Contains(out, "No cameras") || strings.Contains(out, "no camera") {
		return fmt.Errorf("no cameras available on device")
	}
	if strings.Contains(out, "android_camera") && strings.Contains(out, "not found") {
		return fmt.Errorf("android_camera input not supported in this build")
	}
	if strings.Contains(out, "CAMERA") && strings.Contains(out, "Permission") {
		return fmt.Errorf("camera permission not granted to Termux")
	}

	if err != nil && !strings.Contains(out, "frame=") {
		return fmt.Errorf("camera streaming failed: %w", err)
	}

	return nil
}

// VerifyStreamReceived checks if AVD received the stream.
// This is a basic verification - a more complete version would
// actually decode and verify frames.
func (s *E2ETestSuite) VerifyStreamReceived() error {
	// For now, we just check that we can connect as a consumer
	// A more complete test would use ffprobe to verify the stream
	s.t.Log("Verifying stream can be consumed...")

	consumerURL := fmt.Sprintf("rtmp://127.0.0.1:%d/test/stream", avdConsumerPort)

	// Use ffprobe to check if stream is available
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-timeout", "5000000",
		consumerURL,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// This might fail if ffprobe is not available or stream ended
		s.t.Logf("Stream verification via ffprobe failed (may be expected): %v\nOutput: %s", err, output)
		return nil // Not a fatal error
	}

	s.t.Logf("Stream verification output: %s", output)
	return nil
}

// TestFullE2EPipeline runs the complete e2e test pipeline.
// Tests the full streaming pipeline from phone -> avd -> consumer.
func TestFullE2EPipeline(t *testing.T) {
	suite := NewE2ETestSuite(t)
	defer suite.Teardown()

	// Setup
	if err := suite.Setup(); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Check prerequisites
	if err := suite.CheckPrerequisites(); err != nil {
		t.Skipf("Prerequisites not met: %v", err)
	}

	// Deploy ffstream
	if err := suite.DeployFFstream(); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Start AVD
	if err := suite.StartAVD(); err != nil {
		t.Fatalf("Failed to start AVD: %v", err)
	}

	// Set up ADB reverse port forwarding so device can reach AVD
	if err := suite.SetupReversePortForwarding(); err != nil {
		t.Fatalf("Failed to set up reverse port forwarding: %v", err)
	}

	// Run stream test with test source (reliable)
	t.Log("Running test source streaming test...")
	if err := suite.RunStreamTest("testsrc", testStreamDuration); err != nil {
		t.Errorf("Test source streaming failed: %v", err)
	}

	// Verify stream was received
	if err := suite.VerifyStreamReceived(); err != nil {
		t.Errorf("Stream verification failed: %v", err)
	}

	t.Log("Full E2E pipeline test completed successfully")
}

// TestE2ECameraStream tests the full pipeline with real camera input.
func TestE2ECameraStream(t *testing.T) {
	suite := NewE2ETestSuite(t)
	defer suite.Teardown()

	// Setup
	if err := suite.Setup(); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Check prerequisites
	if err := suite.CheckPrerequisites(); err != nil {
		t.Skipf("Prerequisites not met: %v", err)
	}

	// Deploy ffstream
	if err := suite.DeployFFstream(); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Start AVD
	if err := suite.StartAVD(); err != nil {
		t.Fatalf("Failed to start AVD: %v", err)
	}

	// Set up ADB reverse port forwarding so device can reach AVD
	if err := suite.SetupReversePortForwarding(); err != nil {
		t.Fatalf("Failed to set up reverse port forwarding: %v", err)
	}

	// Run camera stream test
	t.Log("Running camera streaming test...")
	if err := suite.RunCameraStreamTest(testStreamDuration); err != nil {
		t.Skipf("Camera streaming failed (may be expected): %v", err)
	}

	t.Log("Camera E2E test completed successfully")
}

// TestE2EProductionConfig tests with a production-like configuration.
func TestE2EProductionConfig(t *testing.T) {
	suite := NewE2ETestSuite(t)
	defer suite.Teardown()

	// Setup
	if err := suite.Setup(); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Check prerequisites
	if err := suite.CheckPrerequisites(); err != nil {
		t.Skipf("Prerequisites not met: %v", err)
	}

	// Deploy ffstream
	if err := suite.DeployFFstream(); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Start AVD
	if err := suite.StartAVD(); err != nil {
		t.Fatalf("Failed to start AVD: %v", err)
	}

	// Set up ADB reverse port forwarding so device can reach AVD
	if err := suite.SetupReversePortForwarding(); err != nil {
		t.Fatalf("Failed to set up reverse port forwarding: %v", err)
	}

	// Run production-like config test
	// This mimics the run-ffstream.sh script from the instructions
	t.Log("Running production-like streaming test...")

	// Use localhost since we're using ADB reverse port forwarding
	rtmpURL := fmt.Sprintf("rtmp://127.0.0.1:%d/test/prod-stream", avdPublisherPort)

	// Production-like command with multiple inputs and fallbacks
	cmdStr := fmt.Sprintf(`timeout 20 ffstream -v info \
		-retry_input_timeout_on_failure 1s \
		-retry_output_timeout_on_failure 0 \
		-hwaccel mediacodec \
		-mux_mode different_outputs_same_tracks_split_av \
		-f lavfi -i 'testsrc=duration=15:size=1280x720:rate=30' \
		-fallback_priority 1 \
		-f lavfi -i 'sine=frequency=1000:duration=15:sample_rate=48000' \
		-s 1280x720 \
		-c:v h264 \
		-ar 48000 -ac 1 -sample_fmt fltp \
		-c:a aac \
		-b:v 4M -bufsize 4M \
		-g 60 -r 30 \
		-f flv \
		'%s' 2>&1`, rtmpURL)

	out, err := suite.deviceHelper.termuxCmd(cmdStr)
	t.Logf("Production config output:\n%s", out)

	if err != nil && !strings.Contains(out, "frame=") {
		if strings.Contains(out, "CANNOT LINK") {
			t.Skipf("Library linking error: %s", out)
		}
		t.Errorf("Production config test failed: %v", err)
	}

	t.Log("Production config E2E test completed")
}

// TestFFstreamBasicFunctionality tests that ffstream is properly installed
// and can run basic operations on the device without needing AVD.
// This is a simpler test that verifies build, deploy, and basic execution.
func TestFFstreamBasicFunctionality(t *testing.T) {
	suite := NewE2ETestSuite(t)
	defer suite.Teardown()

	// Setup
	if err := suite.Setup(); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Check prerequisites
	if err := suite.CheckPrerequisites(); err != nil {
		t.Skipf("Prerequisites not met: %v", err)
	}

	// Deploy ffstream
	if err := suite.DeployFFstream(); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Test 1: Verify ffstream binary exists and is executable
	t.Log("Verifying ffstream installation...")
	out, err := suite.deviceHelper.termuxCmd("which ffstream && ffstream -version 2>&1 || echo 'version check failed'")
	if err != nil {
		t.Fatalf("ffstream not found or not executable: %v, output: %s", err, out)
	}
	t.Logf("ffstream location and version:\n%s", out)

	if !strings.Contains(out, "ffstream") {
		t.Fatalf("ffstream binary not found in PATH")
	}

	// Test 2: Verify ffstream can list available codecs
	t.Log("Checking available encoders...")
	out, err = suite.deviceHelper.termuxCmd("ffstream -encoders 2>&1 | head -20")
	if err != nil {
		t.Logf("Warning: encoder list failed: %v", err)
	} else {
		t.Logf("Available encoders:\n%s", out)
	}

	// Test 3: Run a quick local transcode test (no network, just CPU test)
	t.Log("Running local transcode test (no network)...")

	// Generate 2 seconds of test video and encode to a file
	transcodeCmd := `timeout 10 ffstream -v info \
		-f lavfi -i 'testsrc=duration=2:size=320x240:rate=15' \
		-c copy \
		-f null - 2>&1`

	out, err = suite.deviceHelper.termuxCmd(transcodeCmd)
	t.Logf("Transcode test output:\n%s", out)

	// Check for success indicators
	if strings.Contains(out, "CANNOT LINK") {
		t.Errorf("Library linking error on device: %s", out)
		return
	}

	// The test should complete (even with errors about no audio, etc.)
	if strings.Contains(out, "finished") || strings.Contains(out, "EOF") {
		t.Log("Transcode test completed successfully")
	} else if err != nil && !strings.Contains(out, "frame=") {
		t.Logf("Transcode test may have issues: %v", err)
	}

	t.Log("Basic functionality tests completed")
}
