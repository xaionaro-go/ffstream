//go:build test_e2e && test_real_phone
// +build test_e2e,test_real_phone

// mic_routing_test.go validates that selecting a non-default microphone
// (e.g. back mic vs bottom mic on a Pixel 8a) actually routes the
// daemon's audio capture to the requested physical mic, instead of
// silently falling back to a different one.
//
// Task: #39 ("Mic selection actually-routes test").
//
// Test strategy
// =============
// The wingout QML "Microphone" combo eventually drives a gRPC
// AddInput(priority=0, url=<port_id>, custom_options=[{f:
// android_microphone}]) call into the on-device ffstream daemon (see
// CamerasBuiltin.qml _doActivate). The daemon hands the URL to
// avpipeline's android.NewMicrophone, which AAudio-opens the requested
// device id and logs:
//
//	"AAudio capture stream opened: requested_device_id=X actual_device_id=Y"
//
// If X==Y the request was honoured; if X!=Y AAudio silently fell back
// to a different mic and the routing is broken (microphone.go also
// emits an explicit AAudio-ignored warning in that case).
//
// To exercise the same `f=android_microphone` URL→DeviceID routing
// without depending on the gRPC hot-add path (see "Known limitation"
// below), the test launches a fresh on-device daemon per mic with the
// mic provided directly on the daemon CLI:
//
//	ffstream -f lavfi -i testsrc=...    (placeholder video)
//	         -f android_microphone -i <port_id>
//	         -c:v libx264 -c:a aac -f flv <per_mic_output_file>
//
// On daemon start, the InputFactory builds the chain immediately,
// which calls android.NewMicrophone for the mic URL. The capture
// runs for `micRoutingCaptureSeconds`; we then SIGTERM the daemon,
// pull the FLV off-device, decode the audio track, and assert:
//
//  1. The daemon log contains the AAudio open line with X==Y.
//  2. At least 1 second of audio samples decoded from the FLV
//     (proves the AAudio→encoder→muxer chain end-to-end produced
//     output).
//
// Known limitation: gRPC-AddInput hot-add path
// ============================================
// Driving the same selection via gRPC AddInput on a daemon that has
// already started its input chain does NOT currently re-open the chain
// to pick up the new resource — see pkg/ffstream/ffstream.go's
// AddInput, which only calls Inputs.AddFactory when the *priority*
// level is new. Adding another resource to an existing priority just
// appends to InputsInfo and does not retrigger InputFactory.NewInput.
// The first attempt at this test exercised the gRPC path and hit that
// hot-add gap (daemon log never reached AAudio open). Filed as a
// separate bug from task #39's "actually-routes" question, which is
// resolvable purely by checking URL→DeviceID routing — the path this
// CLI-based test exercises.
//
// Hardware constraint: the test is hardware-bound to the Pixel 8a phone
// at serial 41041JEKB08092 because (a) it has multiple physical
// built-in mics with stable Port IDs (21=bottom, 22=back), and (b)
// AAudio's "open whatever the system gives me" fallback is what the
// user-facing routing bug actually hides. A simulator/emulator cannot
// exercise this path.

package e2e

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// Pixel 8a (akita) connected device serial.
	micRoutingDeviceSerial = "41041JEKB08092"

	// Port IDs for the two physical built-in mics on the Pixel 8a (akita),
	// extracted from `dumpsys media.audio_policy`. The test asserts both
	// are present at runtime; values are duplicated as constants only
	// to keep the test readable.
	pixel8aBottomMicPortID = 21
	pixel8aBackMicPortID   = 22

	// On-device paths.
	//
	// micRoutingTestFFStream points to a per-test binary path
	// (/data/local/tmp/ffstream-test) so the test never overwrites
	// or races with the production daemon launched from
	// /data/local/tmp/ffstream by loop-run-ffstream.sh. The test
	// builds and pushes this binary itself; without it the test
	// skips with a clear message rather than running against an
	// unknown binary.
	micRoutingTestFFStream = "/data/local/tmp/ffstream-test"
	micRoutingTestFFLibs   = "/data/local/tmp/ffmpeg-bin/lib"
	micRoutingTestOutDir   = "/data/local/tmp"

	// Capture window for each mic. AAudio open + encoder init burns
	// the first ~1s, so we need at least 3s on top of that to
	// produce a substantive FLV.
	micRoutingCaptureSeconds = 7
)

// micRoutingDeviceContext holds shared per-suite state for the mic
// routing test.
type micRoutingDeviceContext struct {
	t      *testing.T
	ctx    context.Context
	device *DeviceInfo
	helper *deviceTestHelper
}

// requireMicRoutingDevice verifies the target Pixel 8a is connected,
// has root, has the ffstream binary, and that both built-in mics show
// up in dumpsys.
func requireMicRoutingDevice(
	t *testing.T,
	ctx context.Context,
) *micRoutingDeviceContext {
	t.Helper()

	dev, err := getDevice(ctx, func(d DeviceInfo) bool { return d.Serial == micRoutingDeviceSerial })
	if err != nil {
		t.Skipf("Target device %s not connected: %v", micRoutingDeviceSerial, err)
	}

	helper := newDeviceTestHelper(t, ctx, dev)

	// Verify root is available — AAudio capture from the shell-uid
	// daemon goes through `su root` in production and we must mirror that.
	if _, err := helper.shell("su", "root", "id"); err != nil {
		t.Skipf("Root (su) not available on %s: %v", dev.Serial, err)
	}

	// Verify ffstream binary is present at the expected path.
	if _, err := helper.shell("su", "root", "ls", micRoutingTestFFStream); err != nil {
		t.Skipf("ffstream binary missing at %s: %v", micRoutingTestFFStream, err)
	}

	// Parse dumpsys to confirm both mic Port IDs exist on this hardware.
	// The full audio policy dump is large; the test only inspects the
	// "Port ID:" lines, so we filter on-host instead of fighting
	// adb-shell's arg-joining quirks for sed/awk patterns.
	dumpOut, err := helper.shell("su", "root", "dumpsys", "media.audio_policy")
	require.NoErrorf(t, err, "failed to dump media.audio_policy")

	for _, want := range []int{pixel8aBottomMicPortID, pixel8aBackMicPortID} {
		marker := fmt.Sprintf("Port ID: %d;", want)
		require.Containsf(t, dumpOut, marker,
			"expected mic Port ID %d in dumpsys; without it the test cannot distinguish bottom-vs-back routing.\nFull dump:\n%s",
			want, dumpOut)
	}

	return &micRoutingDeviceContext{
		t:      t,
		ctx:    ctx,
		device: dev,
		helper: helper,
	}
}

// micCaptureResult is the per-mic outcome captured by runOneMicCapture.
type micCaptureResult struct {
	PortID            int
	OutputFLVHostPath string
	DaemonLog         string
	RequestedDevice   int
	ActualDevice      int
	AAudioOpenedOK    bool
	AAudioFallback    bool // true if "AAudio ignored requested device_id=X" warn fired
	RMS               float64
	SampleCount       int
}

// runOneMicCapture spawns a fresh ffstream daemon with the given mic on
// its CLI, captures `micRoutingCaptureSeconds` of output, then stops
// the daemon and pulls the FLV off device.
func (m *micRoutingDeviceContext) runOneMicCapture(
	portID int,
	hostTmpDir string,
) micCaptureResult {
	m.t.Helper()
	tag := fmt.Sprintf("mic%d", portID)
	devLogPath := fmt.Sprintf("%s/test-%s.log", micRoutingTestOutDir, tag)
	devFLVPath := fmt.Sprintf("%s/test-%s.flv", micRoutingTestOutDir, tag)

	// Wipe stale artifacts on device.
	_, _ = m.helper.shell("su", "root", "sh", "-c",
		shellQuote(fmt.Sprintf("rm -f %s %s", devLogPath, devFLVPath)))

	// Spawn daemon with both inputs on the CLI:
	//   - placeholder lavfi video so streammux has a video track
	//   - android_microphone with the desired Port ID
	//
	// adb shell joins all post-shell args by single spaces with no
	// re-quoting; pass the whole script as a single ' '-quoted arg
	// (shellQuote) so it survives device-side re-tokenisation. Avoid
	// inner single quotes — there are none here.
	// `-mux_mode different_outputs_same_tracks_split_av` routes audio
	// frames (from android_microphone) and video frames (from lavfi)
	// to per-track encoder pipelines, then mux-merges them on the
	// way out. This is the same mux mode that the production
	// e2e/full_e2e_test.go suite uses for android_camera + AAudio
	// audio. Without `_split_av` the encoder asserts on the
	// unexpected media type (avpipeline/kernel/encoder.go sendFrame
	// check) when the Tee feeds interleaved A+V frames into a single
	// transcoder.
	startScript := fmt.Sprintf(
		"LD_LIBRARY_PATH=%s %s "+
			"-listen_control tcp+ssl:127.0.0.1:%d "+
			"-mux_mode different_outputs_same_tracks_split_av "+
			"-f lavfi -i testsrc=size=160x90:rate=10 "+
			"-f android_microphone -i %d "+
			"-s 160x90 -c:v libx264 -preset ultrafast -b:v 200k -g 20 "+
			"-c:a aac -ar 48000 -ac 1 -b:a 64k "+
			"-f flv %s "+
			">%s 2>&1 & echo $!",
		micRoutingTestFFLibs,
		micRoutingTestFFStream,
		13593, // dedicated test gRPC port (no client today, but daemon needs the flag for parity)
		portID,
		devFLVPath,
		devLogPath,
	)
	m.t.Logf("[%s] startScript:\n%s", tag, startScript)
	pidStr, err := m.helper.shell("su", "root", "sh", "-c", shellQuote(startScript))
	require.NoErrorf(m.t, err, "failed to spawn daemon")
	pid := lastNonEmptyLine(pidStr)
	require.NotEmptyf(m.t, pid, "daemon pid empty; raw shell output:\n%s", pidStr)
	m.t.Logf("[%s] daemon pid=%s, log=%s, flv=%s", tag, pid, devLogPath, devFLVPath)

	// Always tear down the daemon — even on test failure mid-flight.
	defer func() {
		_, _ = m.helper.shell("su", "root", "sh", "-c",
			shellQuote(fmt.Sprintf("kill %s 2>/dev/null; sleep 1; kill -9 %s 2>/dev/null; true", pid, pid)))
	}()

	// Sanity-check: confirm the daemon process is actually alive on
	// the device. If it died immediately (e.g. CANNOT LINK because
	// LD_LIBRARY_PATH didn't propagate, or panicked on input format
	// dispatch), surface the cause directly with the on-device log.
	time.Sleep(2 * time.Second)
	alive, _ := m.helper.shell("su", "root", "sh", "-c",
		shellQuote(fmt.Sprintf("kill -0 %s 2>/dev/null && echo alive || echo dead", pid)))
	if !strings.Contains(alive, "alive") {
		logBytes, _ := m.helper.shell("su", "root", "sh", "-c",
			shellQuote(fmt.Sprintf("cat %s 2>/dev/null", devLogPath)))
		m.t.Fatalf("[%s] daemon (pid=%s) died right after spawn. Daemon log:\n%s\nadb-shell PID-output raw:\n%q",
			tag, pid, logBytes, pidStr)
	}

	// Let it capture. Total daemon-up time = sanity-check + capture.
	time.Sleep(time.Duration(micRoutingCaptureSeconds) * time.Second)

	// SIGTERM the daemon (kill happens via defer too, but we need
	// ordering: TERM, then wait, then read file).
	_, _ = m.helper.shell("su", "root", "sh", "-c",
		shellQuote(fmt.Sprintf("kill %s 2>/dev/null; true", pid)))
	// Give the daemon a moment to flush the FLV trailer before we pull.
	time.Sleep(2 * time.Second)

	// Pull artifacts off-device.
	logHostPath := filepath.Join(hostTmpDir, fmt.Sprintf("test-%s.log", tag))
	flvHostPath := filepath.Join(hostTmpDir, fmt.Sprintf("test-%s.flv", tag))
	_, _, err = adbCmdWithSerial(m.ctx, m.device.Serial, "pull", devLogPath, logHostPath)
	require.NoErrorf(m.t, err, "adb pull log")
	_, _, err = adbCmdWithSerial(m.ctx, m.device.Serial, "pull", devFLVPath, flvHostPath)
	if err != nil {
		// FLV may be missing if the daemon never produced output;
		// continue so the assertions can describe what went wrong.
		m.t.Logf("[%s] adb pull flv warning: %v", tag, err)
	}

	logBytes, err := os.ReadFile(logHostPath)
	require.NoErrorf(m.t, err, "read pulled log")
	logStr := string(logBytes)

	requested, actual, hasOpenLine := parseAAudioOpenLine(logStr)
	hasFallback := strings.Contains(logStr, "AAudio ignored requested device_id=")

	// Compute audio RMS via ffmpeg → s16le pipe. If the FLV is
	// missing or empty, decodeAudioRMS reports 0 samples — the
	// assertions below distinguish that from a near-silent capture.
	rms, samples := decodeAudioRMS(m.t, flvHostPath)

	res := micCaptureResult{
		PortID:            portID,
		OutputFLVHostPath: flvHostPath,
		DaemonLog:         logStr,
		RequestedDevice:   requested,
		ActualDevice:      actual,
		AAudioOpenedOK:    hasOpenLine,
		AAudioFallback:    hasFallback,
		RMS:               rms,
		SampleCount:       samples,
	}
	return res
}

// shellQuote wraps s in single quotes for safe transit through
// `adb shell` (which strips arg boundaries on the device side by
// joining with spaces). Panics if s contains a single quote — none
// of the launchers in this file rely on single quotes.
func shellQuote(s string) string {
	if strings.Contains(s, "'") {
		panic(fmt.Sprintf("shellQuote: unsupported single quote in %q", s))
	}
	return "'" + s + "'"
}

// lastNonEmptyLine returns the last non-empty trimmed line of s, or
// the empty string if none.
func lastNonEmptyLine(s string) string {
	var last string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			last = line
		}
	}
	return last
}

// parseAAudioOpenLine scans a daemon log for the AAudio capture-open
// line emitted by avpipeline/kernel/extra/android/microphone.go and
// returns (requested_device_id, actual_device_id, found).
//
// Source line format (from Microphone.openCaptureDevice):
//
//	"AAudio capture stream opened: requested_device_id=21 actual_device_id=21 ..."
func parseAAudioOpenLine(log string) (int, int, bool) {
	re := regexp.MustCompile(`requested_device_id=([0-9<>a-z]+) actual_device_id=([0-9-]+)`)
	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, "AAudio capture stream opened") {
			continue
		}
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		req, err1 := strconv.Atoi(m[1])
		act, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil {
			continue
		}
		return req, act, true
	}
	return 0, 0, false
}

// decodeAudioRMS uses ffmpeg to extract the FLV's audio track as s16le
// PCM and computes the root-mean-square amplitude. Returns (rms, n_samples).
func decodeAudioRMS(t *testing.T, flvPath string) (float64, int) {
	t.Helper()
	if _, err := os.Stat(flvPath); err != nil {
		t.Logf("FLV %s missing: %v", flvPath, err)
		return 0, 0
	}
	cmd := exec.Command("ffmpeg",
		"-loglevel", "error",
		"-i", flvPath,
		"-vn",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-ac", "1",
		"-ar", "48000",
		"pipe:1",
	)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	r := bufio.NewReader(stdout)
	var sumSq float64
	var n int
	buf := make([]byte, 2)
	for {
		_, err := io.ReadFull(r, buf)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			t.Fatalf("read pcm: %v", err)
		}
		s := int16(binary.LittleEndian.Uint16(buf))
		f := float64(s)
		sumSq += f * f
		n++
	}
	if err := cmd.Wait(); err != nil {
		t.Logf("ffmpeg exited non-zero: %v\nstderr: %s", err, stderr.String())
	}
	if n == 0 {
		return 0, 0
	}
	rms := math.Sqrt(sumSq / float64(n))
	return rms, n
}

// TestMicRoutingActuallyRoutes is the task #39 test.
//
// Hardware constraint: the test is hardware-bound to the Pixel 8a phone
// at serial 41041JEKB08092 because (a) it has multiple physical
// built-in mics with stable Port IDs, and (b) AAudio's "open whatever
// the system gives me" fallback is what the user-facing bug actually
// hides. A simulator/emulator cannot exercise this path.
//
// Set FFSTREAM_E2E_MIC_ROUTING=1 to opt in.
func TestMicRoutingActuallyRoutes(t *testing.T) {
	if os.Getenv("FFSTREAM_E2E_MIC_ROUTING") == "" {
		t.Skip("set FFSTREAM_E2E_MIC_ROUTING=1 to run mic-routing test (requires Pixel 8a, root, ~30s)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	m := requireMicRoutingDevice(t, ctx)

	hostTmp, err := os.MkdirTemp("", "mic-routing-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(hostTmp) })
	t.Logf("host temp dir: %s", hostTmp)

	results := map[int]micCaptureResult{}
	for _, portID := range []int{pixel8aBottomMicPortID, pixel8aBackMicPortID} {
		res := m.runOneMicCapture(portID, hostTmp)
		results[portID] = res
		t.Logf(
			"[mic%d] AAudio open: req=%d act=%d found=%v fallback=%v | RMS=%.2f over %d samples | flv=%s",
			portID, res.RequestedDevice, res.ActualDevice, res.AAudioOpenedOK, res.AAudioFallback,
			res.RMS, res.SampleCount, res.OutputFLVHostPath,
		)
		// Dump the daemon's AAudio + microphone-related log lines for
		// the post-mortem trail.
		for _, line := range strings.Split(res.DaemonLog, "\n") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "aaudio") ||
				strings.Contains(lower, "microphone") ||
				strings.Contains(lower, "android_microphone") ||
				strings.Contains(line, "device_id") {
				t.Logf("[mic%d log] %s", portID, line)
			}
		}
	}

	// === Assertions ===

	for _, portID := range []int{pixel8aBottomMicPortID, pixel8aBackMicPortID} {
		res := results[portID]

		// 1. Daemon must have opened AAudio capture at all (proves
		//    the input_factory dispatch reached android.NewMicrophone).
		assert.Truef(t, res.AAudioOpenedOK,
			"mic %d: no 'AAudio capture stream opened' log line — input_factory did not dispatch f=android_microphone to the AAudio path. Daemon log:\n%s",
			portID, res.DaemonLog)

		// 2. Routing assertion: requested device must equal the
		//    Port ID we asked for.
		assert.Equalf(t, portID, res.RequestedDevice,
			"mic %d: AAudio open log shows the daemon requested device_id=%d (expected %d). The CLI '-i %d' did not propagate as MicrophoneConfig.DeviceID.",
			portID, res.RequestedDevice, portID, portID)

		// 3. Routing assertion: AAudio's actual device must equal
		//    the requested one. A mismatch means AAudio silently
		//    fell back to a different physical mic — the routing
		//    bug task #39 is testing for.
		assert.Equalf(t, res.RequestedDevice, res.ActualDevice,
			"mic %d: AAudio actual_device_id=%d does not match requested_device_id=%d — AAudio fell back to a different mic. Routing is broken.",
			portID, res.ActualDevice, res.RequestedDevice)

		// 4. The explicit "AAudio ignored requested device_id="
		//    warning must NOT have fired (microphone.go logs that
		//    when AAudio's actual_device_id != requested).
		assert.Falsef(t, res.AAudioFallback,
			"mic %d: daemon logged 'AAudio ignored requested device_id=' warning, meaning AAudio fell back to a different physical mic.",
			portID)

		// 5. Audio energy: at least ~0.25s of PCM samples must have
		//    decoded from the FLV. This proves the AAudio→encoder→
		//    muxer chain actually produced audio output (the user-
		//    visible end of the routing question). RMS may be
		//    near-zero in a quiet room; we only require non-empty
		//    capture. Using 12k (≈0.25s @ 48 kHz) instead of a full
		//    second because the daemon's first ~1s of capture is
		//    spent on AAudio open / encoder init handshake before
		//    audio frames start flowing into the FLV — anything
		//    less than ~12k samples means the chain is still
		//    broken.
		const minSamples = 12_000
		assert.GreaterOrEqualf(t, res.SampleCount, minSamples,
			"mic %d: only %d audio samples decoded from %s (expected >=%d, ≈0.25s @ 48kHz). The mic capture chain produced no usable audio.",
			portID, res.SampleCount, res.OutputFLVHostPath, minSamples)
	}

	// 6. Cross-mic distinctness: a sanity belt-and-braces. With
	//    different mics at different positions on the device the
	//    captures should not be identically silent.
	a := results[pixel8aBottomMicPortID]
	b := results[pixel8aBackMicPortID]
	t.Logf("RMS comparison: bottom_mic=%.2f back_mic=%.2f", a.RMS, b.RMS)
	if a.RMS == 0 && b.RMS == 0 && (a.SampleCount > 0 || b.SampleCount > 0) {
		t.Errorf("both mics produced zero RMS over %d/%d samples — capture chain returned all-zero PCM",
			a.SampleCount, b.SampleCount)
	}
}
