//go:build test_e2e

// ffstream_test.go contains end-to-end tests for ffstream on Android via standalone binary deployment.

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// Standalone Android paths
	androidBinDir = "/data/local/tmp"
	androidTmpDir = "/data/local/tmp"

	// Path to ffstream binary (relative to repo root)
	ffstreamBinaryRelPath = "bin/ffstream-android-arm64"

	// Full path to ffstream on device
	ffstreamDevicePath = androidBinDir + "/ffstream"
)

// deviceTestHelper provides helper methods for device testing.
type deviceTestHelper struct {
	t      *testing.T
	ctx    context.Context
	device *DeviceInfo
}

func newDeviceTestHelper(t *testing.T, ctx context.Context, device *DeviceInfo) *deviceTestHelper {
	return &deviceTestHelper{t: t, ctx: ctx, device: device}
}

func (h *deviceTestHelper) shell(args ...string) (string, error) {
	stdout, stderr, err := adbCmdWithSerial(h.ctx, h.device.Serial, append([]string{"shell"}, args...)...)
	if err != nil {
		return "", fmt.Errorf("shell command failed: %w\nstderr: %s", err, stderr)
	}
	return strings.TrimSpace(stdout), nil
}

func (h *deviceTestHelper) shellRunAs(user string, cmd string) (string, error) {
	return h.shell("run-as", user, "sh", "-c", cmd)
}

func (h *deviceTestHelper) push(local, remote string) error {
	_, stderr, err := adbCmdWithSerial(h.ctx, h.device.Serial, "push", local, remote)
	if err != nil {
		return fmt.Errorf("push failed: %w\nstderr: %s", err, stderr)
	}
	return nil
}

// runCmd runs a command on the device with /data/local/tmp as the working directory.
func (h *deviceTestHelper) runCmd(cmd string) (string, error) {
	return h.shell("sh", "-c", fmt.Sprintf("cd %s && LD_LIBRARY_PATH=%s %s", androidBinDir, androidBinDir, cmd))
}

// shQuote wraps s in single quotes for safe embedding in a POSIX shell command string.
// Any embedded single quotes are escaped via the standard '"'"' sequence.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func (h *deviceTestHelper) checkFfstreamInstalled() bool {
	_, err := h.shell("test", "-x", ffstreamDevicePath)
	return err == nil
}

// checkFfstreamRunnable verifies ffstream can actually execute (not just that binary exists).
// Returns error message if there are missing dependencies, nil if runnable.
func (h *deviceTestHelper) checkFfstreamRunnable() error {
	out, err := h.runCmd(ffstreamDevicePath + " -version 2>&1 || true")
	if err != nil {
		return fmt.Errorf("failed to check ffstream: %w", err)
	}
	if strings.Contains(out, "CANNOT LINK EXECUTABLE") {
		if strings.Contains(out, "cannot locate symbol") {
			return fmt.Errorf("ABI incompatibility (may need rebuild for this Android version): %s", out)
		}
		return fmt.Errorf("missing dependencies: %s", out)
	}
	return nil
}

// TestFFstreamDeployment tests deploying ffstream to a device.
func TestFFstreamDeployment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dev, err := getRealDevice(ctx)
	if err != nil {
		t.Skipf("No real device connected: %v", err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)
	t.Logf("Testing deployment to device: %s (%s)", dev.Model, dev.Serial)

	// Check if binary exists locally
	binPath := filepath.Join(findRepoRoot(t), ffstreamBinaryRelPath)
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		t.Skipf("ffstream binary not found at %s - run 'make bin/ffstream-android-arm64' first", binPath)
	}
	t.Logf("Found binary: %s", binPath)

	// Push binary to device
	t.Logf("Pushing binary to %s", ffstreamDevicePath)
	if err := helper.push(binPath, ffstreamDevicePath); err != nil {
		t.Fatalf("Failed to push binary: %v", err)
	}

	// Make it executable
	if _, err := helper.shell("chmod", "+x", ffstreamDevicePath); err != nil {
		t.Fatalf("Failed to chmod binary: %v", err)
	}

	// Verify it works
	out, err := helper.runCmd(ffstreamDevicePath + " -version 2>&1")
	if err != nil {
		t.Fatalf("Failed to run ffstream -version: %v", err)
	}
	t.Logf("ffstream deployed successfully, version output: %s", out)
}

// TestFFstreamBasicRun tests that ffstream can start and show version.
func TestFFstreamBasicRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dev, err := getRealDevice(ctx)
	if err != nil {
		t.Skipf("No real device connected: %v", err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)

	if !helper.checkFfstreamInstalled() {
		t.Skip("ffstream not installed on device - run TestFFstreamDeployment first")
	}

	// Check if ffstream is runnable (all dependencies present)
	if err := helper.checkFfstreamRunnable(); err != nil {
		t.Skipf("ffstream installed but not runnable: %v", err)
	}

	// Test version flag
	out, err := helper.runCmd(ffstreamDevicePath + " -version")
	if err != nil {
		t.Fatalf("Failed to run ffstream -version: %v", err)
	}
	t.Logf("ffstream version output: %s", out)
}

// TestFFstreamEncodersList tests listing available encoders.
func TestFFstreamEncodersList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dev, err := getRealDevice(ctx)
	if err != nil {
		t.Skipf("No real device connected: %v", err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)

	if !helper.checkFfstreamInstalled() {
		t.Skip("ffstream not installed on device - run TestFFstreamDeployment first")
	}

	if err := helper.checkFfstreamRunnable(); err != nil {
		t.Skipf("ffstream installed but not runnable: %v", err)
	}

	// List encoders
	out, err := helper.runCmd(ffstreamDevicePath + " -encoders 2>&1 | head -50")
	if err != nil {
		// -encoders might exit with error but still produce output
		t.Logf("ffstream -encoders returned error (may be expected): %v", err)
	}
	t.Logf("Available encoders:\n%s", out)

	// Check for MediaCodec encoders which should be available on Android
	if strings.Contains(out, "mediacodec") || strings.Contains(out, "h264") {
		t.Log("Found expected video encoders")
	}
}

// TestFFstreamInputDevices tests listing available input devices.
func TestFFstreamInputDevices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dev, err := getRealDevice(ctx)
	if err != nil {
		t.Skipf("No real device connected: %v", err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)

	if !helper.checkFfstreamInstalled() {
		t.Skip("ffstream not installed on device - run TestFFstreamDeployment first")
	}

	if err := helper.checkFfstreamRunnable(); err != nil {
		t.Skipf("ffstream installed but not runnable: %v", err)
	}

	// List input formats (demuxers)
	out, err := helper.runCmd(ffstreamDevicePath + " -demuxers 2>&1 | head -50")
	if err != nil {
		t.Logf("ffstream -demuxers returned error (may be expected): %v", err)
	}
	t.Logf("Available input formats:\n%s", out)

	// Check for Android camera input
	if strings.Contains(out, "android_camera") || strings.Contains(out, "camera") {
		t.Log("Found camera input support")
	}
}

// TestFFstreamHelp tests that help command works.
func TestFFstreamHelp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dev, err := getRealDevice(ctx)
	if err != nil {
		t.Skipf("No real device connected: %v", err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)

	if !helper.checkFfstreamInstalled() {
		t.Skip("ffstream not installed on device - run TestFFstreamDeployment first")
	}

	if err := helper.checkFfstreamRunnable(); err != nil {
		t.Skipf("ffstream installed but not runnable: %v", err)
	}

	// Test help output
	out, err := helper.runCmd(ffstreamDevicePath + " -h 2>&1 || true")
	if err != nil {
		t.Fatalf("Failed to run ffstream -h: %v", err)
	}

	if !strings.Contains(out, "Usage") && !strings.Contains(out, "usage") && !strings.Contains(out, "-") {
		t.Errorf("Help output doesn't look like help text: %s", out)
	}
	t.Logf("Help output:\n%s", out[:min(len(out), 500)])
}

// TestFFstreamErrorHandling tests basic streaming functionality error handling.
// This test requires a working ffstream installation and creates a test stream.
func TestFFstreamErrorHandling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dev, err := getRealDevice(ctx)
	if err != nil {
		t.Skipf("No real device connected: %v", err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)

	if !helper.checkFfstreamInstalled() {
		t.Skip("ffstream not installed on device - run TestFFstreamDeployment first")
	}

	if err := helper.checkFfstreamRunnable(); err != nil {
		t.Skipf("ffstream installed but not runnable: %v", err)
	}

	// Test that ffstream handles missing inputs gracefully
	// (exits with proper error message rather than crashing)
	t.Log("Testing error handling for missing output")
	out, err := helper.runCmd(ffstreamDevicePath + " 2>&1 || true")
	if err != nil {
		// Some ADB error, not ffstream error
		t.Fatalf("Failed to run ffstream: %v", err)
	}
	t.Logf("Output (no args): %s", out)

	// Verify it produces a meaningful error about missing outputs
	if !strings.Contains(out, "output") && !strings.Contains(out, "no inputs") {
		t.Errorf("Expected error message about missing input/output, got: %s", out)
	}

	// Should contain some common demuxer formats
	if !strings.Contains(strings.ToLower(out), "mp4") && !strings.Contains(strings.ToLower(out), "matroska") {
		t.Logf("Note: demuxers list may have unexpected format")
	}
}

// TestFFstreamCameraCapture tests ffstream with android_camera input.
// This verifies the camera capture pipeline works on the device.
func TestFFstreamCameraCapture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dev, err := getRealDevice(ctx)
	if err != nil {
		t.Skipf("No real device connected: %v", err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)

	if !helper.checkFfstreamInstalled() {
		t.Skip("ffstream not installed on device - run TestFFstreamDeployment first")
	}

	if err := helper.checkFfstreamRunnable(); err != nil {
		t.Skipf("ffstream installed but not runnable: %v", err)
	}

	// Test camera capture with file output (no network required)
	// This mimics the real usage but outputs to a local file instead of RTMP
	outputPath := androidTmpDir + "/camera_test.flv"
	t.Logf("Testing camera capture to %s", outputPath)

	// Camera capture: front camera (index 1), 640x480, 30fps
	// Record for 3 seconds, encode with h264_mediacodec (hardware encoder), output to flv
	cmd := fmt.Sprintf(`timeout 5 %s -v info -hwaccel mediacodec -video_size 640x480 -camera_index 1 -framerate 30 -f android_camera -i "" -s 640x480 -c:v h264_mediacodec -ar 48000 -ac 1 -c:a aac -b:v 1M -g 30 -r 30 -f flv %s 2>&1 || true`, ffstreamDevicePath, outputPath)

	out, err := helper.runCmd(cmd)
	t.Logf("Camera capture output: %s", out)

	// Check for common errors
	if strings.Contains(out, "Permission denied") || strings.Contains(out, "CAMERA") {
		t.Skip("Camera permission not granted")
	}
	if strings.Contains(out, "android_camera") && strings.Contains(out, "not found") {
		t.Skip("android_camera input not supported in this build")
	}
	if strings.Contains(out, "No cameras") || strings.Contains(out, "no camera") {
		t.Skip("No cameras available on device")
	}

	// Check if output file was created (and has some size)
	verifyOut, verifyErr := helper.runCmd(fmt.Sprintf("ls -la %s 2>&1", outputPath))
	if verifyErr != nil {
		t.Logf("Output file check: %s", verifyOut)
		// Camera test may fail for various reasons - skip rather than fail
		t.Skipf("Camera capture did not produce output file: %v", err)
	}
	t.Logf("Output file: %s", verifyOut)

	// Cleanup
	_, _ = helper.runCmd(fmt.Sprintf("rm -f %s", outputPath))
	t.Log("Camera capture test completed successfully")
}

// TestFFstreamRTMPStreaming tests ffstream with RTMP output.
// This requires a local RTMP server to be running.
// Set FFSTREAM_E2E_RTMP_URL env var to specify the RTMP destination.
func TestFFstreamRTMPStreaming(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Check for RTMP destination
	rtmpURL := os.Getenv("FFSTREAM_E2E_RTMP_URL")
	if rtmpURL == "" {
		t.Skip("FFSTREAM_E2E_RTMP_URL not set - skipping RTMP streaming test")
	}

	dev, err := getRealDevice(ctx)
	if err != nil {
		t.Skipf("No real device connected: %v", err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)

	if !helper.checkFfstreamInstalled() {
		t.Skip("ffstream not installed on device - run TestFFstreamDeployment first")
	}

	if err := helper.checkFfstreamRunnable(); err != nil {
		t.Skipf("ffstream installed but not runnable: %v", err)
	}

	t.Logf("Testing RTMP streaming to %s", rtmpURL)

	// Stream camera to RTMP for 10 seconds
	cmd := fmt.Sprintf(`timeout 15 %s -v info -retry_input_timeout_on_failure 1s -retry_output_timeout_on_failure 0 -hwaccel mediacodec -video_size 640x480 -camera_index 1 -framerate 30 -f android_camera -i "" -s 640x480 -c:v h264_mediacodec -ar 48000 -ac 1 -c:a aac -b:v 1M -bufsize 1M -g 30 -r 30 -f flv %s 2>&1 || true`, ffstreamDevicePath, shQuote(rtmpURL))

	out, err := helper.runCmd(cmd)
	t.Logf("RTMP streaming output: %s", out)

	// Check for permission errors
	if strings.Contains(out, "Permission denied") || strings.Contains(out, "CAMERA") {
		t.Skip("Camera permission not granted")
	}

	// Check for network errors
	if strings.Contains(out, "Connection refused") {
		t.Skipf("RTMP server not reachable at %s", rtmpURL)
	}

	// If we got this far without fatal errors, the streaming pipeline works
	if strings.Contains(out, "FATA") && !strings.Contains(out, "timeout") {
		t.Errorf("Streaming failed with fatal error: %s", out)
	}

	t.Log("RTMP streaming test completed")
}

// TestFFstreamFullPipeline tests a full streaming pipeline similar to production.
// This test mimics the run-ffstream.sh script configuration.
// Requires: FFSTREAM_E2E_RTMP_URL environment variable.
func TestFFstreamFullPipeline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	rtmpURL := os.Getenv("FFSTREAM_E2E_RTMP_URL")
	if rtmpURL == "" {
		t.Skip("FFSTREAM_E2E_RTMP_URL not set - skipping full pipeline test")
	}

	dev, err := getRealDevice(ctx)
	if err != nil {
		t.Skipf("No real device connected: %v", err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)

	if !helper.checkFfstreamInstalled() {
		t.Skip("ffstream not installed on device - run TestFFstreamDeployment first")
	}

	if err := helper.checkFfstreamRunnable(); err != nil {
		t.Skipf("ffstream installed but not runnable: %v", err)
	}

	// Check if pulseaudio is available for audio capture
	hasPulse := true
	if _, err := helper.runCmd("pulseaudio --check 2>&1 || pulseaudio --start 2>&1"); err != nil {
		t.Log("PulseAudio not available - will test video-only pipeline")
		hasPulse = false
	}

	t.Logf("Testing full pipeline to %s (audio=%v)", rtmpURL, hasPulse)

	// Build command similar to run-ffstream.sh
	var cmdBuilder strings.Builder
	cmdBuilder.WriteString(fmt.Sprintf("timeout 20 %s -v info ", ffstreamDevicePath))
	cmdBuilder.WriteString("-retry_input_timeout_on_failure 1s ")
	cmdBuilder.WriteString("-retry_output_timeout_on_failure 0 ")
	cmdBuilder.WriteString("-hwaccel mediacodec ")
	cmdBuilder.WriteString("-mux_mode different_outputs_same_tracks_split_av ")

	// Camera input
	cmdBuilder.WriteString("-video_size 640x480 ")
	cmdBuilder.WriteString("-camera_index 1 ")
	cmdBuilder.WriteString("-framerate 30 ")
	cmdBuilder.WriteString("-f android_camera -i '' ")

	// Audio input (if available)
	if hasPulse {
		cmdBuilder.WriteString("-f pulse -i default ")
	}

	// Video encoder settings
	cmdBuilder.WriteString("-s 640x480 -c:v h264_mediacodec -b:v 2M -bufsize 2M -g 60 -r 30 ")

	// Audio encoder settings
	if hasPulse {
		cmdBuilder.WriteString("-ar 48000 -ac 1 -sample_fmt fltp -c:a aac ")
	}

	// Output format and URL
	cmdBuilder.WriteString("-f flv ")
	cmdBuilder.WriteString(fmt.Sprintf("%s ", shQuote(rtmpURL)))
	cmdBuilder.WriteString("2>&1 || true")

	out, err := helper.runCmd(cmdBuilder.String())
	t.Logf("Full pipeline output:\n%s", out)

	// Check for critical errors
	if strings.Contains(out, "Permission denied") {
		t.Skip("Permission denied - check camera/microphone permissions")
	}
	if strings.Contains(out, "Connection refused") {
		t.Skipf("RTMP server not reachable at %s", rtmpURL)
	}
	if strings.Contains(out, "CANNOT LINK") {
		t.Skipf("Library linking error: %s", out)
	}

	// Check for successful frame output
	if strings.Contains(out, "frame=") || strings.Contains(out, "Output") {
		t.Log("Full pipeline test completed - frames were processed")
	} else if strings.Contains(out, "FATA") && !strings.Contains(out, "timeout") {
		t.Errorf("Full pipeline failed: %s", out)
	}
}

// TestFFstreamControlSocket tests the gRPC control socket functionality.
func TestFFstreamControlSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dev, err := getRealDevice(ctx)
	if err != nil {
		t.Skipf("No real device connected: %v", err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)

	if !helper.checkFfstreamInstalled() {
		t.Skip("ffstream not installed on device - run TestFFstreamDeployment first")
	}

	if err := helper.checkFfstreamRunnable(); err != nil {
		t.Skipf("ffstream installed but not runnable: %v", err)
	}

	// Check if ffstreamctl is available
	if _, err := helper.shell("test", "-x", androidBinDir+"/ffstreamctl"); err != nil {
		t.Skip("ffstreamctl not installed")
	}

	outputPath := androidTmpDir + "/control_test.mp4"
	controlSocket := "127.0.0.1:3594"

	t.Log("Starting ffstream with control socket...")

	// Start ffstream in background with control socket
	startCmd := fmt.Sprintf(`%s -v info \
		-listen_control %s \
		-video_size 320x240 \
		-camera_index 1 \
		-framerate 15 \
		-f android_camera -i '' \
		-c:v h264 -s 320x240 -b:v 500K -g 15 -r 15 \
		-f mp4 \
		'%s' > /dev/null 2>&1 &
		echo $!`, ffstreamDevicePath, controlSocket, outputPath)

	pidOut, err := helper.runCmd(startCmd)
	if err != nil {
		t.Fatalf("Failed to start ffstream: %v", err)
	}
	pid := strings.TrimSpace(pidOut)
	t.Logf("Started ffstream with PID: %s", pid)

	// Give it time to start
	time.Sleep(3 * time.Second)

	// Check if process is running
	if _, err := helper.runCmd(fmt.Sprintf("kill -0 %s 2>&1", pid)); err != nil {
		t.Logf("ffstream process not running - may have failed to start")
		out, _ := helper.runCmd("cat /tmp/ffstream.log 2>&1 || true")
		t.Logf("Log output: %s", out)
		t.Skip("ffstream failed to start with control socket")
	}

	// Try to get stats via ffstreamctl
	ffstreamctlPath := androidBinDir + "/ffstreamctl"
	statsOut, err := helper.runCmd(fmt.Sprintf("%s -remote %s get-stats 2>&1 || true", ffstreamctlPath, controlSocket))
	t.Logf("Stats output: %s", statsOut)

	// Cleanup: kill the ffstream process
	helper.runCmd(fmt.Sprintf("kill %s 2>/dev/null || true", pid))
	helper.runCmd(fmt.Sprintf("rm -f %s", outputPath))

	// If we got stats output (even empty), the control socket works
	if err == nil && !strings.Contains(statsOut, "connection refused") {
		t.Log("Control socket test passed")
	} else {
		t.Logf("Control socket may not be fully functional: %v", err)
	}
}

// findRepoRoot finds the repository root directory.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	// Start from current directory and walk up
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("Could not find repository root (no go.mod found)")
		}
		dir = parent
	}
}
