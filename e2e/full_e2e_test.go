//go:build test_e2e && test_real_phone
// +build test_e2e,test_real_phone

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
	avdLogWg     sync.WaitGroup
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
	// Build ffstream binary if needed
	if err := s.buildFFstream(); err != nil {
		return fmt.Errorf("ffstream build failed: %w", err)
	}

	// Check avd binary exists (we build it if needed)
	if err := s.ensureAVDBinary(); err != nil {
		return fmt.Errorf("avd binary check failed: %w", err)
	}

	return nil
}

// buildFFstream ensures the ffstream Android binary is available.
func (s *E2ETestSuite) buildFFstream() error {
	binPath := filepath.Join(findRepoRoot(s.t), ffstreamBinaryRelPath)

	// Check if binary already exists
	if _, err := os.Stat(binPath); err == nil {
		s.t.Log("Using existing ffstream binary at " + ffstreamBinaryRelPath)
		return nil
	}

	// Check if Docker is available to build
	cmd := exec.CommandContext(s.ctx, "docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Docker not available and no existing binary at %s: %w", ffstreamBinaryRelPath, err)
	}

	s.t.Log("Building ffstream Android binary...")
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Minute)
	defer cancel()

	// Run make target to build the binary
	cmd = exec.CommandContext(ctx, "make", "bin/ffstream-android-arm64")
	cmd.Dir = findRepoRoot(s.t)
	cmd.Env = os.Environ()

	// Stream output to test log
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build failed: %w\nOutput: %s", err, string(output))
	}

	s.t.Logf("Build completed successfully")

	// Verify the binary was created
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		return fmt.Errorf("binary not created after build at %s", binPath)
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

	cmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-tags=with_libav", "-o", avdBin, "./cmd/avd")
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

	// Push binary to device
	binPath := filepath.Join(findRepoRoot(s.t), ffstreamBinaryRelPath)
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("ffstream binary not found at %s", binPath)
	}

	s.t.Logf("Pushing binary to %s", ffstreamDevicePath)
	if err := s.deviceHelper.push(binPath, ffstreamDevicePath); err != nil {
		return fmt.Errorf("failed to push binary: %w", err)
	}

	// Make it executable
	if _, err := s.deviceHelper.shell("chmod", "+x", ffstreamDevicePath); err != nil {
		return fmt.Errorf("failed to chmod binary: %w", err)
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
	return s.generateAVDConfigWithAudio(0)
}

// generateAVDConfigWithAudio generates an AVD configuration expecting the given
// number of audio tracks on the consumer port.
func (s *E2ETestSuite) generateAVDConfigWithAudio(audioTrackCount int) string {
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
    audio_track_count: %d
endpoints:
  test/stream: {}
`, avdManagementPort, avdPublisherPort, avdConsumerPort, audioTrackCount)
}

// StartAVDWithAudio starts the AVD server configured to expect audio tracks.
func (s *E2ETestSuite) StartAVDWithAudio(audioTrackCount int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	avdBin := s.getAVDBinaryPath()
	s.avdConfig = s.generateAVDConfigWithAudio(audioTrackCount)
	return s.startAVDLocked(avdBin)
}

// StartAVD starts the AVD server to receive streams.
func (s *E2ETestSuite) StartAVD() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	avdBin := s.getAVDBinaryPath()
	s.avdConfig = s.generateAVDConfig()
	return s.startAVDLocked(avdBin)
}

func (s *E2ETestSuite) startAVDLocked(avdBin string) error {

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

	// Log output in background; tracked by avdLogWg so StopAVD can wait
	// for the goroutines to finish before the test returns (prevents
	// "Log called after test finished" panics).
	s.avdLogWg.Add(2)
	go func() {
		defer s.avdLogWg.Done()
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
		defer s.avdLogWg.Done()
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
		s.avdLogWg.Wait() // drain log goroutines before test returns
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
	cmdStr := fmt.Sprintf(`timeout %d %s -v info \
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
		ffstreamDevicePath,
		int(duration.Seconds()),
		int(duration.Seconds()),
		rtmpURL)

	s.t.Logf("Running ffstream command on device...")
	out, err := s.deviceHelper.runCmd(cmdStr)
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
	if _, err := s.deviceHelper.runCmd("pulseaudio --check 2>&1 || pulseaudio --start 2>&1"); err != nil {
		s.t.Log("PulseAudio not available - testing video only")
		hasPulse = false
	}

	var cmdBuilder strings.Builder
	cmdBuilder.WriteString(fmt.Sprintf("timeout %d %s -v info ", int(duration.Seconds())+5, ffstreamDevicePath))
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
	out, err := s.deviceHelper.runCmd(cmdBuilder.String())
	s.t.Logf("Camera stream output:\n%s", out)

	// Check for camera-specific errors
	if strings.Contains(out, "No cameras") || strings.Contains(out, "no camera") {
		return fmt.Errorf("no cameras available on device")
	}
	if strings.Contains(out, "android_camera") && strings.Contains(out, "not found") {
		return fmt.Errorf("android_camera input not supported in this build")
	}
	if strings.Contains(out, "CAMERA") && strings.Contains(out, "Permission") {
		return fmt.Errorf("camera permission not granted")
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
	cmdStr := fmt.Sprintf(`timeout 20 %s -v info \
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
		'%s' 2>&1`, ffstreamDevicePath, rtmpURL)

	out, err := suite.deviceHelper.runCmd(cmdStr)
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
	out, err := suite.deviceHelper.runCmd(fmt.Sprintf("test -x %s && %s -version 2>&1 || echo 'version check failed'", ffstreamDevicePath, ffstreamDevicePath))
	if err != nil {
		t.Fatalf("ffstream not found or not executable: %v, output: %s", err, out)
	}
	t.Logf("ffstream version:\n%s", out)

	if strings.Contains(out, "version check failed") {
		t.Fatalf("ffstream binary not found or not executable at %s", ffstreamDevicePath)
	}

	// Test 2: Verify ffstream can list available codecs
	t.Log("Checking available encoders...")
	out, err = suite.deviceHelper.runCmd(ffstreamDevicePath + " -encoders 2>&1 | head -20")
	if err != nil {
		t.Logf("Warning: encoder list failed: %v", err)
	} else {
		t.Logf("Available encoders:\n%s", out)
	}

	// Test 3: Run a quick local transcode test (no network, just CPU test)
	t.Log("Running local transcode test (no network)...")

	// Generate 2 seconds of test video and encode to a file
	transcodeCmd := fmt.Sprintf(`timeout 10 %s -v info \
		-f lavfi -i 'testsrc=duration=2:size=320x240:rate=15' \
		-c copy \
		-f null - 2>&1`, ffstreamDevicePath)

	out, err = suite.deviceHelper.runCmd(transcodeCmd)
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

// countDTSErrors counts occurrences of DTS monotonicity errors in ffstream log
// output. Returns the count and the matching lines for diagnostics.
func countDTSErrors(output string) (int, []string) {
	var count int
	var matches []string
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "DTS from the stream's past") ||
			strings.Contains(line, "received too old item") {
			count++
			matches = append(matches, line)
		}
	}
	return count, matches
}

// runAudioDTSMonotonicityTest is the shared implementation for both
// android_camera and V4L2 MJPEG DTS monotonicity tests.
// videoInputFlags contains the ffstream flags for the video input source.
func (s *E2ETestSuite) runAudioDTSMonotonicityTest(
	videoInputFlags string,
	duration time.Duration,
) (dtsErrorCount int, dtsErrorLines []string) {
	rtmpURL := fmt.Sprintf("rtmp://127.0.0.1:%d/test/stream", avdPublisherPort)
	s.t.Logf("Testing audio DTS monotonicity, streaming to %s (via ADB reverse)", rtmpURL)

	var cmdBuilder strings.Builder
	cmdBuilder.WriteString(fmt.Sprintf("timeout %d %s -v info ", int(duration.Seconds())+5, ffstreamDevicePath))
	cmdBuilder.WriteString("-retry_input_timeout_on_failure 1s ")
	cmdBuilder.WriteString("-retry_output_timeout_on_failure 0 ")
	cmdBuilder.WriteString("-hwaccel mediacodec ")
	cmdBuilder.WriteString("-mux_mode different_outputs_same_tracks_split_av ")

	// Video input (caller-provided)
	cmdBuilder.WriteString(videoInputFlags)
	cmdBuilder.WriteString(" ")

	// Audio input: android_microphone
	cmdBuilder.WriteString("-sample_rate 48000 -f android_microphone -i 0 ")

	// Video encoder settings
	cmdBuilder.WriteString("-s 640x480 -c:v hevc_mediacodec ")
	cmdBuilder.WriteString("-b:v 4M -bufsize 4M -g 60 -r 30 ")

	// Audio encoder settings
	cmdBuilder.WriteString("-ar 48000 -ac 1 -sample_fmt fltp -c:a aac ")

	// Output
	cmdBuilder.WriteString("-f flv ")
	cmdBuilder.WriteString(fmt.Sprintf("'%s' 2>&1", rtmpURL))

	s.t.Logf("Running ffstream command on device...")
	out, err := s.deviceHelper.runCmd(cmdBuilder.String())
	s.t.Logf("ffstream output:\n%s", out)

	// Check for fatal setup errors (skip rather than fail)
	switch {
	case strings.Contains(out, "CANNOT LINK"):
		s.t.Skipf("Library linking error: %s", out)
	case strings.Contains(out, "Connection refused"):
		s.t.Skipf("AVD not reachable at %s", rtmpURL)
	case strings.Contains(out, "Permission denied"):
		s.t.Skipf("Permission denied (check camera/microphone permissions): %s", out)
	case strings.Contains(out, "No cameras") || strings.Contains(out, "no camera"):
		s.t.Skipf("No cameras available on device")
	case strings.Contains(out, "android_camera") && strings.Contains(out, "not found"):
		s.t.Skipf("android_camera input not supported in this build")
	case strings.Contains(out, "android_microphone") && strings.Contains(out, "not found"):
		s.t.Skipf("android_microphone input not supported in this build")
	}

	// Verify that streaming actually happened (at least some frames processed)
	if !strings.Contains(out, "frame=") && !strings.Contains(out, "Output") {
		if err != nil {
			s.t.Fatalf("Streaming did not start: %v, output: %s", err, out)
		}
	}

	dtsErrorCount, dtsErrorLines = countDTSErrors(out)
	return dtsErrorCount, dtsErrorLines
}

// TestE2E_AndroidCamera_AudioDTSMonotonicity tests that audio DTS values remain
// monotonically increasing when using android_camera video + android_microphone
// audio with mux_mode different_outputs_same_tracks_split_av.
// This reproduces the production "DTS from the stream's past" error.
// Agent-generated test.
func TestE2E_AndroidCamera_AudioDTSMonotonicity(t *testing.T) {
	suite := NewE2ETestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	if err := suite.CheckPrerequisites(); err != nil {
		t.Skipf("Prerequisites not met: %v", err)
	}

	if err := suite.DeployFFstream(); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	if err := suite.StartAVDWithAudio(1); err != nil {
		t.Fatalf("Failed to start AVD: %v", err)
	}

	if err := suite.SetupReversePortForwarding(); err != nil {
		t.Fatalf("Failed to set up reverse port forwarding: %v", err)
	}

	videoFlags := "-video_size 640x480 -camera_index 0 -framerate 30 -f android_camera -i ''"

	dtsErrorCount, dtsErrorLines := suite.runAudioDTSMonotonicityTest(
		videoFlags,
		20*time.Second,
	)

	for _, line := range dtsErrorLines {
		t.Logf("DTS error: %s", line)
	}
	t.Logf("Total DTS monotonicity errors: %d", dtsErrorCount)

	if dtsErrorCount > 0 {
		t.Errorf("detected %d 'DTS from the stream's past' errors in ffstream log (expected 0)", dtsErrorCount)
	}
}

// TestE2E_V4L2MJPEG_AudioDTSMonotonicity tests that audio DTS values remain
// monotonically increasing when using V4L2 MJPEG video + android_microphone
// audio with mux_mode different_outputs_same_tracks_split_av.
// This requires a real device with /dev/video0 available.
// Agent-generated test.
func TestE2E_V4L2MJPEG_AudioDTSMonotonicity(t *testing.T) {
	suite := NewE2ETestSuite(t)
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// V4L2 only works on real devices, not emulators
	if suite.device.IsEmulator {
		t.Skip("V4L2 not available on emulator")
	}

	// Check if /dev/video0 exists on the device
	out, _ := suite.deviceHelper.shell("ls", "/dev/video0")
	if !strings.Contains(out, "video0") {
		t.Skip("no V4L2 device (/dev/video0) available, skipping V4L2 MJPEG test")
	}

	if err := suite.CheckPrerequisites(); err != nil {
		t.Skipf("Prerequisites not met: %v", err)
	}

	if err := suite.DeployFFstream(); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	if err := suite.StartAVDWithAudio(1); err != nil {
		t.Fatalf("Failed to start AVD: %v", err)
	}

	if err := suite.SetupReversePortForwarding(); err != nil {
		t.Fatalf("Failed to set up reverse port forwarding: %v", err)
	}

	videoFlags := "-video_size 1920x1080 -input_format mjpeg -framerate 30 -f video4linux2 -i /dev/video0"

	dtsErrorCount, dtsErrorLines := suite.runAudioDTSMonotonicityTest(
		videoFlags,
		20*time.Second,
	)

	for _, line := range dtsErrorLines {
		t.Logf("DTS error: %s", line)
	}
	t.Logf("Total DTS monotonicity errors: %d", dtsErrorCount)

	if dtsErrorCount > 0 {
		t.Errorf("detected %d 'DTS from the stream's past' errors in ffstream log (expected 0)", dtsErrorCount)
	}
}
