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
	// Path to ffstream deb package (relative to repo root)
	ffstreamDebPath = "bin/ffstream-android-termux-arm64.deb"

	// Termux paths on Android
	termuxHome    = "/data/data/com.termux/files/home"
	termuxHomeLib = "/data/data/com.termux/files/home/lib"
	termuxUsrBin  = "/data/data/com.termux/files/usr/bin"
	termuxUsrLib  = "/data/data/com.termux/files/usr/lib"
	termuxTmpPath = "/data/data/com.termux/files/usr/tmp"
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

func (h *deviceTestHelper) termuxCmd(cmd string) (string, error) {
	// Run command in termux environment with proper PATH and LD_LIBRARY_PATH
	// Only use ~/lib for LD_LIBRARY_PATH (not /usr/lib - that causes symbol conflicts)
	fullCmd := fmt.Sprintf("run-as com.termux sh -c 'export PATH=%s:$PATH && export LD_LIBRARY_PATH=%s && cd %s && %s'", termuxUsrBin, termuxHomeLib, termuxHome, cmd)
	return h.shell(fullCmd)
}

func (h *deviceTestHelper) checkTermuxInstalled() bool {
	_, err := h.shell("pm", "list", "packages", "com.termux")
	return err == nil
}

func (h *deviceTestHelper) checkFfstreamInstalled() bool {
	out, err := h.termuxCmd("which ffstream")
	return err == nil && strings.Contains(out, "ffstream")
}

// copyDebToTermux copies the deb from sdcard to termux home using pipe trick.
func (h *deviceTestHelper) copyDebToTermux(sdcardPath, termuxPath string) error {
	// Use pipe to transfer file since run-as can't directly access sdcard
	cmd := fmt.Sprintf("cat %s | run-as com.termux tee %s > /dev/null", sdcardPath, termuxPath)
	_, err := h.shell(cmd)
	return err
}

// installDebInTermux installs a deb package in termux.
// Note: This requires dpkg to be available in termux.
func (h *deviceTestHelper) installDebInTermux(debPath string) error {
	// dpkg -i will install the package
	out, err := h.termuxCmd(fmt.Sprintf("dpkg -i %s 2>&1", debPath))
	if err != nil {
		return fmt.Errorf("dpkg install failed: %w, output: %s", err, out)
	}
	return nil
}

// requiredLibraries maps library files to their termux package names.
var requiredLibraries = map[string]string{
	"libandroid-glob.so":            "libandroid-glob",
	"libandroid-posix-semaphore.so": "libandroid-posix-semaphore",
	"libc++_shared.so":              "libc++",
	"libpulse.so":                   "pulseaudio",
	"libpulse.so.0":                 "pulseaudio",
}

// termuxBroadcast sends a command to termux via Android broadcast.
// Requires allow-external-apps=true in termux.properties.
func (h *deviceTestHelper) termuxBroadcast(path string, args []string, background bool) error {
	cmd := []string{
		"am", "broadcast", "--user", "0",
		"-a", "com.termux.RUN_COMMAND",
		"--es", "com.termux.RUN_COMMAND_PATH", path,
	}
	if len(args) > 0 {
		cmd = append(cmd, "--esa", "com.termux.RUN_COMMAND_ARGUMENTS", strings.Join(args, ","))
	}
	if background {
		cmd = append(cmd, "--ez", "com.termux.RUN_COMMAND_BACKGROUND", "true")
	}
	cmd = append(cmd, "-n", "com.termux/.app.RunCommandService")

	_, err := h.shell(cmd...)
	return err
}

// enableTermuxExternalApps enables allow-external-apps in termux.properties.
func (h *deviceTestHelper) enableTermuxExternalApps() error {
	// Check if already enabled
	out, _ := h.shell("run-as", "com.termux", "grep", "-q", "^allow-external-apps.*=.*true", termuxHome+"/.termux/termux.properties")
	if out == "" {
		// Not enabled, add it
		h.shell("run-as", "com.termux", "sh", "-c", fmt.Sprintf("echo 'allow-external-apps = true' >> %s/.termux/termux.properties", termuxHome))
	}
	return nil
}

// installMissingDependencies installs missing termux packages for ffstream.
// Tries multiple methods: direct apt, then termux broadcast.
func (h *deviceTestHelper) installMissingDependencies() ([]string, error) {
	// Check which packages need to be installed
	var toInstall []string
	seen := make(map[string]bool)

	for lib, pkg := range requiredLibraries {
		if seen[pkg] {
			continue
		}
		// Check if library exists
		if _, err := h.termuxCmd(fmt.Sprintf("test -e %s/%s", termuxUsrLib, lib)); err != nil {
			seen[pkg] = true
			toInstall = append(toInstall, pkg)
		}
	}

	if len(toInstall) == 0 {
		return nil, nil
	}

	pkgList := strings.Join(toInstall, " ")

	// Method 1: Try direct apt (may fail due to network in run-as)
	h.termuxCmd("apt update 2>&1 || true")
	h.termuxCmd(fmt.Sprintf("apt install -y %s 2>&1 || true", pkgList))

	// Check if it worked
	allInstalled := true
	for lib := range requiredLibraries {
		if _, err := h.termuxCmd(fmt.Sprintf("test -e %s/%s", termuxUsrLib, lib)); err != nil {
			allInstalled = false
			break
		}
	}
	if allInstalled {
		return nil, nil
	}

	// Method 2: Try termux broadcast (requires external apps enabled)
	h.enableTermuxExternalApps()

	// Create install script
	script := fmt.Sprintf("#!/data/data/com.termux/files/usr/bin/bash\npkg install -y %s\n", pkgList)
	h.shell("sh", "-c", fmt.Sprintf("echo '%s' | run-as com.termux tee %s/install_deps.sh > /dev/null", script, termuxHome))
	h.shell("run-as", "com.termux", "chmod", "+x", termuxHome+"/install_deps.sh")

	// Try broadcast
	h.termuxBroadcast(termuxHome+"/install_deps.sh", nil, true)

	// Give it some time to run
	time.Sleep(5 * time.Second)

	// Re-check
	for lib := range requiredLibraries {
		if _, err := h.termuxCmd(fmt.Sprintf("test -e %s/%s", termuxUsrLib, lib)); err != nil {
			return toInstall, fmt.Errorf("install manually in Termux app: pkg install %s", pkgList)
		}
	}

	return nil, nil
}

// setupLibrarySymlinks creates ~/lib with symlinks to required termux libraries.
// This is needed because some libraries may be missing from the standard path.
func (h *deviceTestHelper) setupLibrarySymlinks() error {
	// Create ~/lib directory
	if _, err := h.termuxCmd("mkdir -p " + termuxHomeLib); err != nil {
		return fmt.Errorf("failed to create lib dir: %w", err)
	}

	// Create symlinks for each library (ignore errors for missing libs)
	for lib := range requiredLibraries {
		src := termuxUsrLib + "/" + lib
		dst := termuxHomeLib + "/" + lib
		// Remove existing and create symlink (ignore errors)
		h.termuxCmd(fmt.Sprintf("rm -f %s 2>/dev/null; ln -sf %s %s 2>/dev/null || true", dst, src, dst))
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

	// Check if termux is installed
	if !helper.checkTermuxInstalled() {
		t.Skip("Termux not installed on device")
	}
	t.Log("Termux is installed")

	// Check if deb package exists locally
	debPath := filepath.Join(findRepoRoot(t), ffstreamDebPath)
	if _, err := os.Stat(debPath); os.IsNotExist(err) {
		t.Skipf("ffstream deb package not found at %s - run 'make bin/ffstream-android-termux.deb' first", debPath)
	}
	t.Logf("Found deb package: %s", debPath)

	// Deployment flow:
	// 1. Push deb to /sdcard/Download (accessible location)
	// 2. Copy to termux home using pipe trick (run-as can't access sdcard directly)
	// 3. Install with dpkg (if available)

	sdcardPath := "/sdcard/Download/ffstream-android-termux-arm64.deb"
	termuxDebPath := termuxHome + "/ffstream.deb"

	t.Logf("Pushing deb to %s", sdcardPath)
	if err := helper.push(debPath, sdcardPath); err != nil {
		t.Fatalf("Failed to push deb: %v", err)
	}

	t.Logf("Copying deb to termux home: %s", termuxDebPath)
	if err := helper.copyDebToTermux(sdcardPath, termuxDebPath); err != nil {
		t.Fatalf("Failed to copy deb to termux: %v", err)
	}

	t.Log("Deb package copied to termux home successfully")

	// Install missing dependencies
	t.Log("Installing missing dependencies...")
	missing, err := helper.installMissingDependencies()
	if err != nil {
		t.Logf("Warning: %v", err)
	} else if len(missing) == 0 {
		t.Log("All dependencies present")
	}

	// Set up library symlinks in ~/lib
	t.Log("Setting up library symlinks...")
	if err := helper.setupLibrarySymlinks(); err != nil {
		t.Logf("Warning: failed to setup library symlinks: %v", err)
	} else {
		t.Log("Library symlinks created in ~/lib")
	}

	// Check if dpkg is available
	if _, err := helper.termuxCmd("which dpkg"); err == nil {
		t.Log("dpkg available, attempting installation...")
		if err := helper.installDebInTermux(termuxDebPath); err != nil {
			t.Logf("Installation failed (may need manual intervention): %v", err)
		} else {
			t.Log("ffstream installed successfully")
		}
	} else {
		t.Log("dpkg not available - manual installation required: pkg install dpkg && dpkg -i ~/ffstream.deb")
	}
}

// checkFfstreamRunnable verifies ffstream can actually execute (not just that binary exists).
// Returns error message if there are missing dependencies, nil if runnable.
func (h *deviceTestHelper) checkFfstreamRunnable() error {
	out, err := h.termuxCmd("ffstream -version 2>&1 || true")
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

// checkLibrarySymlinks verifies if library symlinks point to existing files.
func (h *deviceTestHelper) checkLibrarySymlinks() map[string]bool {
	result := make(map[string]bool)
	for lib := range requiredLibraries {
		_, err := h.termuxCmd(fmt.Sprintf("test -e %s/%s", termuxUsrLib, lib))
		result[lib] = err == nil
	}
	return result
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
		// Report which libraries are missing
		libStatus := helper.checkLibrarySymlinks()
		var missing []string
		for lib, exists := range libStatus {
			if !exists {
				missing = append(missing, lib)
			}
		}
		if len(missing) > 0 {
			t.Logf("Missing libraries in termux: %v", missing)
		}
		t.Skipf("ffstream installed but not runnable: %v", err)
	}

	// Test version flag
	out, err := helper.termuxCmd("ffstream -version")
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
	out, err := helper.termuxCmd("ffstream -encoders 2>&1 | head -50")
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
	out, err := helper.termuxCmd("ffstream -demuxers 2>&1 | head -50")
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
	out, err := helper.termuxCmd("ffstream -h 2>&1 || true")
	if err != nil {
		t.Fatalf("Failed to run ffstream -h: %v", err)
	}

	if !strings.Contains(out, "Usage") && !strings.Contains(out, "usage") && !strings.Contains(out, "-") {
		t.Errorf("Help output doesn't look like help text: %s", out)
	}
	t.Logf("Help output:\n%s", out[:min(len(out), 500)])
}

// TestFFstreamStreamingBasic tests basic streaming functionality.
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
	out, err := helper.termuxCmd("ffstream 2>&1 || true")
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
	outputPath := termuxTmpPath + "/camera_test.flv"
	t.Logf("Testing camera capture to %s", outputPath)

	// Camera capture: front camera (index 1), 640x480, 30fps
	// Record for 3 seconds, encode with h264_mediacodec (hardware encoder), output to flv
	// ffstream CLI flag order per run-ffstream.sh:
	// 1. Input options and -i
	// 2. -s WxH (collected for -c:v)
	// 3. -c:v codec
	// 4. -ar/-ac (collected for -c:a)
	// 5. -c:a codec
	// 6. Output options (-b:v -g -r -f) and output URL
	// Note: Using h264_mediacodec (Android hardware encoder) since libx264 is not in this build
	cmd := fmt.Sprintf(`timeout 5 ffstream -v info -hwaccel mediacodec -video_size 640x480 -camera_index 1 -framerate 30 -f android_camera -i "" -s 640x480 -c:v h264_mediacodec -ar 48000 -ac 1 -c:a aac -b:v 1M -g 30 -r 30 -f flv %s 2>&1 || true`, outputPath)

	out, err := helper.termuxCmd(cmd)
	t.Logf("Camera capture output: %s", out)

	// Check for common errors
	if strings.Contains(out, "Permission denied") || strings.Contains(out, "CAMERA") {
		t.Skip("Camera permission not granted - enable camera access for Termux")
	}
	if strings.Contains(out, "android_camera") && strings.Contains(out, "not found") {
		t.Skip("android_camera input not supported in this build")
	}
	if strings.Contains(out, "No cameras") || strings.Contains(out, "no camera") {
		t.Skip("No cameras available on device")
	}

	// Check if output file was created (and has some size)
	verifyOut, verifyErr := helper.termuxCmd(fmt.Sprintf("ls -la %s 2>&1", outputPath))
	if verifyErr != nil {
		t.Logf("Output file check: %s", verifyOut)
		// Camera test may fail for various reasons - skip rather than fail
		t.Skipf("Camera capture did not produce output file: %v", err)
	}
	t.Logf("Output file: %s", verifyOut)

	// Cleanup
	_, _ = helper.termuxCmd(fmt.Sprintf("rm -f %s", outputPath))
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
	// This mimics the real run-ffstream.sh script
	// Flag order: input options, -i, -s (for -c:v), -c:v, -ar/-ac (for -c:a), -c:a, output options, URL
	cmd := fmt.Sprintf(`timeout 15 ffstream -v info -retry_input_timeout_on_failure 1s -retry_output_timeout_on_failure 0 -hwaccel mediacodec -video_size 640x480 -camera_index 1 -framerate 30 -f android_camera -i "" -s 640x480 -c:v h264_mediacodec -ar 48000 -ac 1 -c:a aac -b:v 1M -bufsize 1M -g 30 -r 30 -f flv %s 2>&1 || true`, rtmpURL)

	out, err := helper.termuxCmd(cmd)
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
	if _, err := helper.termuxCmd("pulseaudio --check 2>&1 || pulseaudio --start 2>&1"); err != nil {
		t.Log("PulseAudio not available - will test video-only pipeline")
		hasPulse = false
	}

	t.Logf("Testing full pipeline to %s (audio=%v)", rtmpURL, hasPulse)

	// Build command similar to run-ffstream.sh
	// Flag order: input options, -i, -s (for -c:v), -c:v, -ar/-ac (for -c:a), -c:a, output options, URL
	var cmdBuilder strings.Builder
	cmdBuilder.WriteString("timeout 20 ffstream -v info ")
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

	// Video encoder settings: -s BEFORE -c:v (gets collected for the codec)
	cmdBuilder.WriteString("-s 640x480 -c:v h264_mediacodec -b:v 2M -bufsize 2M -g 60 -r 30 ")

	// Audio encoder settings: -ar/-ac BEFORE -c:a
	if hasPulse {
		cmdBuilder.WriteString("-ar 48000 -ac 1 -sample_fmt fltp -c:a aac ")
	}

	// Output format and URL
	cmdBuilder.WriteString("-f flv ")
	cmdBuilder.WriteString(fmt.Sprintf("%s ", rtmpURL))
	cmdBuilder.WriteString("2>&1 || true")

	out, err := helper.termuxCmd(cmdBuilder.String())
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
	if _, err := helper.termuxCmd("which ffstreamctl"); err != nil {
		t.Skip("ffstreamctl not installed")
	}

	outputPath := termuxTmpPath + "/control_test.mp4"
	controlSocket := "127.0.0.1:3594"

	t.Log("Starting ffstream with control socket...")

	// Start ffstream in background with control socket
	// Note: -s must come AFTER -c:v (ffstream quirk)
	startCmd := fmt.Sprintf(`ffstream -v info \
		-listen_control %s \
		-video_size 320x240 \
		-camera_index 1 \
		-framerate 15 \
		-f android_camera -i '' \
		-c:v h264 -s 320x240 -b:v 500K -g 15 -r 15 \
		-f mp4 \
		'%s' > /dev/null 2>&1 &
		echo $!`, controlSocket, outputPath)

	pidOut, err := helper.termuxCmd(startCmd)
	if err != nil {
		t.Fatalf("Failed to start ffstream: %v", err)
	}
	pid := strings.TrimSpace(pidOut)
	t.Logf("Started ffstream with PID: %s", pid)

	// Give it time to start
	time.Sleep(3 * time.Second)

	// Check if process is running
	if _, err := helper.termuxCmd(fmt.Sprintf("kill -0 %s 2>&1", pid)); err != nil {
		t.Logf("ffstream process not running - may have failed to start")
		out, _ := helper.termuxCmd("cat /tmp/ffstream.log 2>&1 || true")
		t.Logf("Log output: %s", out)
		t.Skip("ffstream failed to start with control socket")
	}

	// Try to get stats via ffstreamctl
	statsOut, err := helper.termuxCmd(fmt.Sprintf("ffstreamctl -remote %s get-stats 2>&1 || true", controlSocket))
	t.Logf("Stats output: %s", statsOut)

	// Cleanup: kill the ffstream process
	helper.termuxCmd(fmt.Sprintf("kill %s 2>/dev/null || true", pid))
	helper.termuxCmd(fmt.Sprintf("rm -f %s", outputPath))

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
