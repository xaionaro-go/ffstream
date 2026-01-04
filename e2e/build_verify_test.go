// build_verify_test.go verifies the build process and configuration for end-to-end tests.

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// BuildConfig holds build configuration options.
type BuildConfig struct {
	EnableVLC        bool
	EnableLibSRT     bool
	EnableDebugTrace bool
	PassthroughAVP   bool
}

// BuildManager handles ffstream build operations.
type BuildManager struct {
	t       *testing.T
	ctx     context.Context
	repoDir string
	config  BuildConfig
}

// NewBuildManager creates a new build manager.
func NewBuildManager(t *testing.T, ctx context.Context) *BuildManager {
	return &BuildManager{
		t:       t,
		ctx:     ctx,
		repoDir: findRepoRoot(t),
		config:  BuildConfig{},
	}
}

// CheckDebPackageExists checks if the Android deb package exists.
func (b *BuildManager) CheckDebPackageExists() bool {
	debPath := filepath.Join(b.repoDir, ffstreamDebPath)
	_, err := os.Stat(debPath)
	return err == nil
}

// GetDebPackagePath returns the path to the deb package.
func (b *BuildManager) GetDebPackagePath() string {
	return filepath.Join(b.repoDir, ffstreamDebPath)
}

// GetDebPackageInfo returns information about the deb package.
func (b *BuildManager) GetDebPackageInfo() (map[string]string, error) {
	debPath := b.GetDebPackagePath()
	if _, err := os.Stat(debPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("deb package not found at %s", debPath)
	}

	info := map[string]string{
		"path": debPath,
	}

	// Get file size
	stat, err := os.Stat(debPath)
	if err == nil {
		info["size"] = fmt.Sprintf("%d bytes", stat.Size())
		info["modified"] = stat.ModTime().Format(time.RFC3339)
	}

	return info, nil
}

// BuildDebPackage builds the Android deb package.
// Note: This requires the Docker builder to be available.
func (b *BuildManager) BuildDebPackage() error {
	b.t.Log("Building ffstream Android deb package...")
	b.t.Log("This requires Docker and the Android builder image.")

	// Check if Docker is available
	cmd := exec.CommandContext(b.ctx, "docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Docker not available: %w", err)
	}

	// Run make target
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Minute)
	defer cancel()

	cmd = exec.CommandContext(ctx, "make", "bin/ffstream-android-arm64")
	cmd.Dir = b.repoDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("ENABLE_VLC=%v", b.config.EnableVLC),
		fmt.Sprintf("ENABLE_DEBUG_TRACE=%v", b.config.EnableDebugTrace),
		fmt.Sprintf("PASSTHROUGH_AVPIPELINE=%v", b.config.PassthroughAVP),
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build failed: %w\nOutput: %s", err, output)
	}

	b.t.Logf("Build output: %s", output)

	// Verify the package was created
	if !b.CheckDebPackageExists() {
		return fmt.Errorf("deb package not created after build")
	}

	return nil
}

// TestBuildStatus tests the build status without actually building.
func TestBuildStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	bm := NewBuildManager(t, ctx)

	if bm.CheckDebPackageExists() {
		info, err := bm.GetDebPackageInfo()
		if err != nil {
			t.Fatalf("Failed to get package info: %v", err)
		}
		t.Logf("ffstream deb package exists:")
		for k, v := range info {
			t.Logf("  %s: %s", k, v)
		}
	} else {
		t.Logf("ffstream deb package not found at %s", bm.GetDebPackagePath())
		t.Log("Run 'make bin/ffstream-android-arm64' to build it")
	}
}

// StreamVerifier handles stream verification.
type StreamVerifier struct {
	t   *testing.T
	ctx context.Context
}

// NewStreamVerifier creates a new stream verifier.
func NewStreamVerifier(t *testing.T, ctx context.Context) *StreamVerifier {
	return &StreamVerifier{t: t, ctx: ctx}
}

// FFProbeResult represents ffprobe output.
type FFProbeResult struct {
	Streams []struct {
		Index        int    `json:"index"`
		CodecName    string `json:"codec_name"`
		CodecType    string `json:"codec_type"`
		Width        int    `json:"width,omitempty"`
		Height       int    `json:"height,omitempty"`
		SampleRate   string `json:"sample_rate,omitempty"`
		Channels     int    `json:"channels,omitempty"`
		AvgFrameRate string `json:"avg_frame_rate,omitempty"`
		DurationTs   int64  `json:"duration_ts,omitempty"`
		Duration     string `json:"duration,omitempty"`
	} `json:"streams"`
	Format struct {
		Filename   string `json:"filename"`
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
}

// VerifyRTMPStream verifies an RTMP stream is valid using ffprobe.
func (v *StreamVerifier) VerifyRTMPStream(rtmpURL string, timeout time.Duration) (*FFProbeResult, error) {
	ctx, cancel := context.WithTimeout(v.ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		"-timeout", fmt.Sprintf("%d", int(timeout.Seconds())*1000000),
		rtmpURL,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var result FFProbeResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	return &result, nil
}

// VerifyStreamHasVideo checks if the stream has a valid video track.
func (v *StreamVerifier) VerifyStreamHasVideo(result *FFProbeResult) error {
	for _, stream := range result.Streams {
		if stream.CodecType == "video" {
			if stream.Width > 0 && stream.Height > 0 {
				v.t.Logf("Found video stream: %s %dx%d", stream.CodecName, stream.Width, stream.Height)
				return nil
			}
		}
	}
	return fmt.Errorf("no valid video stream found")
}

// VerifyStreamHasAudio checks if the stream has a valid audio track.
func (v *StreamVerifier) VerifyStreamHasAudio(result *FFProbeResult) error {
	for _, stream := range result.Streams {
		if stream.CodecType == "audio" {
			if stream.SampleRate != "" {
				v.t.Logf("Found audio stream: %s %s Hz", stream.CodecName, stream.SampleRate)
				return nil
			}
		}
	}
	return fmt.Errorf("no valid audio stream found")
}

// RecordStream records a portion of the stream to a file for verification.
func (v *StreamVerifier) RecordStream(rtmpURL, outputPath string, duration time.Duration) error {
	ctx, cancel := context.WithTimeout(v.ctx, duration+10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y",
		"-i", rtmpURL,
		"-t", fmt.Sprintf("%d", int(duration.Seconds())),
		"-c", "copy",
		outputPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to record stream: %w\nOutput: %s", err, output)
	}

	// Verify output file exists and has size
	stat, err := os.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("output file not created: %w", err)
	}
	if stat.Size() == 0 {
		return fmt.Errorf("output file is empty")
	}

	v.t.Logf("Recorded %d bytes to %s", stat.Size(), outputPath)
	return nil
}

// VerifyRecordedFile verifies a recorded file is valid.
func (v *StreamVerifier) VerifyRecordedFile(filePath string) (*FFProbeResult, error) {
	ctx, cancel := context.WithTimeout(v.ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		filePath,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	var result FFProbeResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	return &result, nil
}

// TestStreamVerification is a standalone test for stream verification.
// Set FFSTREAM_E2E_RTMP_URL to test against a running stream.
func TestStreamVerification(t *testing.T) {
	rtmpURL := os.Getenv("FFSTREAM_E2E_RTMP_URL")
	if rtmpURL == "" {
		t.Skip("FFSTREAM_E2E_RTMP_URL not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	verifier := NewStreamVerifier(t, ctx)

	// Verify stream
	result, err := verifier.VerifyRTMPStream(rtmpURL, 10*time.Second)
	if err != nil {
		t.Fatalf("Stream verification failed: %v", err)
	}

	// Check video
	if err := verifier.VerifyStreamHasVideo(result); err != nil {
		t.Errorf("Video check failed: %v", err)
	}

	// Check audio (optional)
	if err := verifier.VerifyStreamHasAudio(result); err != nil {
		t.Logf("Audio check: %v", err)
	}
}

// NetworkHelper provides network-related utilities for e2e tests.
type NetworkHelper struct {
	t   *testing.T
	ctx context.Context
}

// NewNetworkHelper creates a new network helper.
func NewNetworkHelper(t *testing.T, ctx context.Context) *NetworkHelper {
	return &NetworkHelper{t: t, ctx: ctx}
}

// CheckPortAvailable checks if a port is available for binding.
func (n *NetworkHelper) CheckPortAvailable(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	listener.Close()
	return true
}

// WaitForPort waits for a port to become available.
func (n *NetworkHelper) WaitForPort(host string, port int, timeout time.Duration) error {
	addr := fmt.Sprintf("%s:%d", host, port)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("port %d not available after %v", port, timeout)
}

// GetContainerHostIP returns the IP address of the host from container's perspective.
func (n *NetworkHelper) GetContainerHostIP() (string, error) {
	// Try common Docker bridge gateway IPs
	candidates := []string{"172.17.0.1", "host.docker.internal"}

	for _, ip := range candidates {
		if n.isReachable(ip) {
			return ip, nil
		}
	}

	return "", fmt.Errorf("no reachable host IP found")
}

func (n *NetworkHelper) isReachable(ip string) bool {
	conn, err := net.DialTimeout("tcp", ip+":5037", 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// TestNetworkSetup tests the network configuration for e2e tests.
func TestNetworkSetup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	helper := NewNetworkHelper(t, ctx)

	// Check if required ports are available
	ports := []int{avdPublisherPort, avdConsumerPort, avdManagementPort}
	for _, port := range ports {
		if helper.CheckPortAvailable(port) {
			t.Logf("Port %d is available", port)
		} else {
			t.Logf("Port %d is in use", port)
		}
	}

	// Get host IP
	hostIP, err := helper.GetContainerHostIP()
	if err != nil {
		t.Logf("Could not determine host IP: %v", err)
	} else {
		t.Logf("Host IP: %s", hostIP)
	}
}
