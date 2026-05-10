// flag_test.go contains tests for command-line flags.
package main

import (
	"context"
	"reflect"
	"testing"

	xlogrus "github.com/facebookincubator/go-belt/tool/logger/implementation/logrus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
)

// testFatalPanic is the value the test-only ExitFunc panics with so the
// recover-based assertion in TestParseFlags_AutoBitRateResolution_Con-
// flictsWithMaxHeight can distinguish a project-fatal from an
// unrelated panic.
type testFatalPanic struct{}

func init() {
	// Override DefaultLogrusLogger so parseFlags' Fatal path raises a
	// recoverable panic instead of os.Exit(1) — required to assert
	// fatal-on-conflict in-process. xlogrus' Fatalf reads
	// logrusEntry.Logger.ExitFunc, so installing a panicking ExitFunc
	// on the returned logger is sufficient. Existing tests pass valid
	// args and never hit Fatal, so the global override is benign.
	prev := xlogrus.DefaultLogrusLogger
	xlogrus.DefaultLogrusLogger = func() *logrus.Logger {
		l := prev()
		l.ExitFunc = func(int) { panic(testFatalPanic{}) }
		return l
	}
}

func TestResourcesByFallbackPriority(t *testing.T) {
	t.Run("nilOnEmpty", func(t *testing.T) {
		var s ffstream.Resources
		if got := s.ByFallbackPriority(); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})

	t.Run("groupsAndSorts", func(t *testing.T) {
		s := ffstream.Resources{
			{URL: "a", Priority: 2},
			{URL: "b", Priority: 1},
			{URL: "c", Priority: 1},
			{URL: "d"}, // priority 0 (default)
			{URL: "e", Priority: 2},
		}

		got := s.ByFallbackPriority()
		want := []ffstream.Resources{
			{{URL: "d"}}, // priority 0
			{
				{URL: "b", Priority: 1},
				{URL: "c", Priority: 1},
			},
			{
				{URL: "a", Priority: 2},
				{URL: "e", Priority: 2},
			},
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected result\nwant: %#v\n got: %#v", want, got)
		}
	})
}

// TestParseFlags_ZeroInputsZeroOutputs_NoFatal pins the contract
// established alongside ffstream.Start's zero-state idle path (commit
// 0801de0): parseFlags must NOT fatal-exit when invoked with no
// positional outputs and no -i inputs. The rc.local supervisor model
// boots ffstream-289 idle with just -listen_control and expects
// wingout to drive activation later via gRPC AddInput +
// SetOutputURL. A flag-parser-level fatal here would defeat the
// loosened ffstream.Start guard. Falsification: reverting the
// guard at flag.go:198 to a fatal makes this test trip
// runParseFlagsCatchFatal.
func TestParseFlags_ZeroInputsZeroOutputs_NoFatal(t *testing.T) {
	args := []string{
		"ffstream",
		"-listen_control", "tcp:127.0.0.1:3594",
	}
	if runParseFlagsCatchFatal(t, args) {
		t.Fatalf("parseFlags fatal-exited on zero-input/zero-output argv; expected idle parse")
	}
	_, flags := parseFlags(args)
	if len(flags.Inputs) != 0 {
		t.Errorf("expected zero Inputs, got %d (%#v)", len(flags.Inputs), flags.Inputs)
	}
	if len(flags.Outputs) != 0 {
		t.Errorf("expected zero Outputs, got %d (%#v)", len(flags.Outputs), flags.Outputs)
	}
	if flags.ListenControlSocket != "tcp:127.0.0.1:3594" {
		t.Errorf("ListenControlSocket: want %q, got %q",
			"tcp:127.0.0.1:3594", flags.ListenControlSocket)
	}
}

func TestParseFlags_ExitOnLastInputRemoved(t *testing.T) {
	args := []string{
		"ffstream",
		"-exit_on_last_input_removed", "true",
		"-listen_control", "tcp:127.0.0.1:3594",
	}

	_, flags := parseFlags(args)
	require.True(t, flags.ExitOnLastInputRemoved)
}

// TestParseFlags_TCPMSS_InRange parses an in-range -tcp_mss flag and
// pins it lands in Flags.TCPMSS unchanged. Guards the impl-1 type-
// domain validator path under normal use.
//
// Broke-the-code-validation: replacing validateTCPMSS with a constant
// (e.g. always 0) breaks this test.
func TestParseFlags_TCPMSS_InRange(t *testing.T) {
	args := []string{
		"ffstream",
		"-tcp_mss", "1200",
		"-listen_control", "tcp:127.0.0.1:3594",
	}
	_, flags := parseFlags(args)
	require.Equal(t, 1200, flags.TCPMSS,
		"-tcp_mss=1200 must be preserved verbatim in Flags.TCPMSS")
}

// TestParseFlags_TCPMSS_OutOfRange_Fatal pins the impl-1 fix: a
// -tcp_mss value above 65535 (the TCP MSS option's 16-bit unsigned
// ceiling per RFC 793 §3.1) must be rejected at parseFlags before
// reaching the int(uint64) narrowing. Caught via the test-only
// fatal-as-panic injection in init().
//
// Broke-the-code-validation: removing validateTCPMSS or its bounds
// check (e.g. silently casting uint64 → int with no validation) lets
// TCPMSS=70000 land in Flags.TCPMSS, which the int() narrowing then
// drops to a residual value depending on platform — silent corruption.
func TestParseFlags_TCPMSS_OutOfRange_Fatal(t *testing.T) {
	args := []string{
		"ffstream",
		"-tcp_mss", "70000",
		"-listen_control", "tcp:127.0.0.1:3594",
	}
	if !runParseFlagsCatchFatal(t, args) {
		t.Fatalf("parseFlags must fatal on -tcp_mss > 65535 (RFC 793 §3.1 16-bit limit); did not")
	}
}

func TestParseFlags_Suppressed(t *testing.T) {
	args := []string{"ffstream", "-suppressed", "true", "-i", "rtsp://input1", "rtmp://output"}
	_, flags := parseFlags(args)

	if len(flags.Inputs) != 1 {
		t.Fatalf("expected 1 input, got %d", len(flags.Inputs))
	}

	if !flags.Inputs[0].Suppressed {
		t.Errorf("expected Suppressed to be true for input1")
	}

	if flags.Inputs[0].URL != "rtsp://input1" {
		t.Errorf("expected URL to be rtsp://input1, got %q", flags.Inputs[0].URL)
	}
}

// TestParseFlags_QueueSize verifies that the four -queue_size_* flags are
// registered with the parser (not silently passed through as unknown
// options to the trailing output URL) and that their values land in the
// matching Flags fields. The deprecated -queue_size_default knob is also
// covered: parseFlags simply preserves its raw value in
// Flags.QueueSizeDefault — the per-role fan-out lives in main.go (see the
// transcoderInput / outputInput / *Error block in main()), not here.
func TestParseFlags_QueueSize(t *testing.T) {
	const outputURL = "rtmp://output"

	// queueLeak inspects flags.Outputs[0].CustomOptions for any entry whose
	// Key matches one of the registered queue-size flag names. Such a match
	// would mean the parser failed to consume the flag and convertUnknown-
	// OptionsToAVPCustomOptions later turned the leftover "-queue_size_*
	// N" pair into a custom option attached to the output URL — exactly
	// the regression we want to catch.
	queueLeak := func(t *testing.T, opts avptypes.DictionaryItems) {
		t.Helper()
		for _, opt := range opts {
			switch opt.Key {
			case "queue_size_default",
				"queue_size_transcoder",
				"queue_size_output",
				"queue_size_error":
				t.Errorf("queue-size flag leaked into output CustomOptions as %q=%q", opt.Key, opt.Value)
			}
		}
	}

	cases := []struct {
		name     string
		flagName string
		value    string
		check    func(t *testing.T, f Flags)
	}{
		{
			name:     "transcoder",
			flagName: "-queue_size_transcoder",
			value:    "7",
			check: func(t *testing.T, f Flags) {
				if f.QueueSizeTranscoder != 7 {
					t.Errorf("QueueSizeTranscoder: want 7, got %d", f.QueueSizeTranscoder)
				}
				// Untargeted siblings stay at the zero sentinel.
				if f.QueueSizeOutput != 0 || f.QueueSizeError != 0 || f.QueueSizeDefault != 0 {
					t.Errorf("untargeted siblings should be 0, got default=%d output=%d error=%d",
						f.QueueSizeDefault, f.QueueSizeOutput, f.QueueSizeError)
				}
			},
		},
		{
			name:     "output",
			flagName: "-queue_size_output",
			value:    "11",
			check: func(t *testing.T, f Flags) {
				if f.QueueSizeOutput != 11 {
					t.Errorf("QueueSizeOutput: want 11, got %d", f.QueueSizeOutput)
				}
				if f.QueueSizeTranscoder != 0 || f.QueueSizeError != 0 || f.QueueSizeDefault != 0 {
					t.Errorf("untargeted siblings should be 0, got default=%d transcoder=%d error=%d",
						f.QueueSizeDefault, f.QueueSizeTranscoder, f.QueueSizeError)
				}
			},
		},
		{
			name:     "error",
			flagName: "-queue_size_error",
			value:    "3",
			check: func(t *testing.T, f Flags) {
				if f.QueueSizeError != 3 {
					t.Errorf("QueueSizeError: want 3, got %d", f.QueueSizeError)
				}
				if f.QueueSizeTranscoder != 0 || f.QueueSizeOutput != 0 || f.QueueSizeDefault != 0 {
					t.Errorf("untargeted siblings should be 0, got default=%d transcoder=%d output=%d",
						f.QueueSizeDefault, f.QueueSizeTranscoder, f.QueueSizeOutput)
				}
			},
		},
		{
			name:     "deprecatedDefault",
			flagName: "-queue_size_default",
			value:    "5",
			check: func(t *testing.T, f Flags) {
				// parseFlags is the wrong layer to exercise the fan-out:
				// the deprecated -queue_size_default knob is preserved
				// verbatim here, and main.go reads it together with the
				// per-role flags to derive the effective queue sizes.
				if f.QueueSizeDefault != 5 {
					t.Errorf("QueueSizeDefault: want 5, got %d", f.QueueSizeDefault)
				}
				if f.QueueSizeTranscoder != 0 || f.QueueSizeOutput != 0 || f.QueueSizeError != 0 {
					t.Errorf("per-role fields must stay 0 at parseFlags layer (fan-out is in main.go); got transcoder=%d output=%d error=%d",
						f.QueueSizeTranscoder, f.QueueSizeOutput, f.QueueSizeError)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"ffstream", "-i", "rtsp://input1", tc.flagName, tc.value, outputURL}
			_, flags := parseFlags(args)

			tc.check(t, flags)

			// Pass-through regression check: registered flags must not
			// leak into the trailing output's CustomOptions.
			if len(flags.Outputs) != 1 {
				t.Fatalf("expected exactly 1 output, got %d (%#v)", len(flags.Outputs), flags.Outputs)
			}
			if got := flags.Outputs[0].URL; got != outputURL {
				t.Errorf("output URL: want %q, got %q", outputURL, got)
			}
			queueLeak(t, flags.Outputs[0].CustomOptions)
		})
	}
}

// TestParseFlags_QueueSize_AllAtOnce covers the realistic case where the
// operator overrides every per-role flag in a single invocation: every
// field must reflect its argument independently, with no cross-talk
// between flags.
func TestParseFlags_QueueSize_AllAtOnce(t *testing.T) {
	args := []string{
		"ffstream",
		"-i", "rtsp://input1",
		"-queue_size_transcoder", "7",
		"-queue_size_output", "11",
		"-queue_size_error", "3",
		"-queue_size_default", "5",
		"rtmp://output",
	}
	_, flags := parseFlags(args)

	if flags.QueueSizeTranscoder != 7 {
		t.Errorf("QueueSizeTranscoder: want 7, got %d", flags.QueueSizeTranscoder)
	}
	if flags.QueueSizeOutput != 11 {
		t.Errorf("QueueSizeOutput: want 11, got %d", flags.QueueSizeOutput)
	}
	if flags.QueueSizeError != 3 {
		t.Errorf("QueueSizeError: want 3, got %d", flags.QueueSizeError)
	}
	if flags.QueueSizeDefault != 5 {
		t.Errorf("QueueSizeDefault: want 5, got %d", flags.QueueSizeDefault)
	}

	if len(flags.Outputs) != 1 {
		t.Fatalf("expected exactly 1 output, got %d (%#v)", len(flags.Outputs), flags.Outputs)
	}
	for _, opt := range flags.Outputs[0].CustomOptions {
		switch opt.Key {
		case "queue_size_default",
			"queue_size_transcoder",
			"queue_size_output",
			"queue_size_error":
			t.Errorf("queue-size flag leaked into output CustomOptions as %q=%q", opt.Key, opt.Value)
		}
	}
}

// TestParseFlags_QueueSize_SentinelZero pins the documented sentinel: when
// none of the four queue-size flags are passed, all four fields stay at
// uint64(0), which main.go then forwards as "leave the avpipeline
// compiled-in default" for every channel.
func TestParseFlags_QueueSize_SentinelZero(t *testing.T) {
	args := []string{"ffstream", "-i", "rtsp://input1", "rtmp://output"}
	_, flags := parseFlags(args)

	if flags.QueueSizeDefault != 0 {
		t.Errorf("QueueSizeDefault: want 0, got %d", flags.QueueSizeDefault)
	}
	if flags.QueueSizeTranscoder != 0 {
		t.Errorf("QueueSizeTranscoder: want 0, got %d", flags.QueueSizeTranscoder)
	}
	if flags.QueueSizeOutput != 0 {
		t.Errorf("QueueSizeOutput: want 0, got %d", flags.QueueSizeOutput)
	}
	if flags.QueueSizeError != 0 {
		t.Errorf("QueueSizeError: want 0, got %d", flags.QueueSizeError)
	}
}

// TestParseFlags_AutoBitRateResolution pins the -auto_bitrate_resolution
// override behavior: passing "WxH" must collapse the ladder to a single
// entry at that exact resolution while preserving the codec-default
// MinBitRate/MaxBitRate band on that entry.
func TestParseFlags_AutoBitRateResolution(t *testing.T) {
	args := []string{
		"ffstream",
		"-c:v", "libx264", "-b:v", "4000000",
		"-auto_bitrate", "true",
		"-auto_bitrate_resolution", "1920x1080",
		"-i", "test://in", "test://out",
	}
	_, flags := parseFlags(args)
	require.NotNil(t, flags.AutoBitRate)
	require.Len(t, flags.AutoBitRate.ResolutionsAndBitRates, 1)
	e := flags.AutoBitRate.ResolutionsAndBitRates[0]
	require.Equal(t, uint32(1920), e.Width)
	require.Equal(t, uint32(1080), e.Height)
	require.Equal(t, flags.AutoBitRate.MinBitRate, e.BitrateLow)
	require.Equal(t, flags.AutoBitRate.MaxBitRate, e.BitrateHigh)
}

// TestParseFlags_AutoBitRateResolution_DefaultLadderRetained pins the
// negative case: without -auto_bitrate_resolution the ladder must keep
// multiple entries (the codec-default rungs). We pick a non-Forbid
// MuxMode to skip the unrelated "collapse to Best" path so we can
// observe the true default ladder shape.
func TestParseFlags_AutoBitRateResolution_DefaultLadderRetained(t *testing.T) {
	args := []string{
		"ffstream",
		"-c:v", "libx264", "-b:v", "4000000",
		"-auto_bitrate", "true",
		"-mux_mode", "same_output_same_tracks",
		"-i", "test://in", "test://out",
	}
	_, flags := parseFlags(args)
	require.NotNil(t, flags.AutoBitRate)
	require.Greater(
		t, len(flags.AutoBitRate.ResolutionsAndBitRates), 1,
		"default ladder must keep multiple entries when no -auto_bitrate_resolution is set",
	)
}

// TestParseFlags_AutoBitRateResolution_ConflictsWithMaxHeight pins the
// parse-time conflict-detection contract: passing
// -auto_bitrate_resolution alongside operator-passed
// -auto_bitrate_max_height (or -auto_bitrate_min_height) must abort
// via fatal(), since the pin would otherwise silently override the
// height filter. Detection now uses ffflag.Option.Changed() — the
// authoritative "operator typed this flag in argv" signal — instead
// of equality-against-default. The pre-fix test had a
// "pinPlusDefaultHeights_ok" case that exercised the equality
// workaround; under Changed() that case must FATAL because the
// operator did pass the flag (regardless of whether the value
// happens to equal the registered default), so it has been merged
// into the fatal cases.
func TestParseFlags_AutoBitRateResolution_ConflictsWithMaxHeight(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantFatal bool
	}{
		{
			name: "pinPlusMaxHeight_fatal",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1920x1080",
				"-auto_bitrate_max_height", "720",
				"-i", "test://in", "test://out",
			},
			wantFatal: true,
		},
		{
			name: "pinPlusMinHeight_fatal",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1920x1080",
				"-auto_bitrate_min_height", "240",
				"-i", "test://in", "test://out",
			},
			wantFatal: true,
		},
		{
			name: "pinAlone_ok",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1920x1080",
				"-i", "test://in", "test://out",
			},
			wantFatal: false,
		},
		{
			name: "pinPlusOperatorEchoesDefault_fatal",
			// Operator passes the SAME values as the registered
			// defaults. Pre-fix this was tolerated (equality with
			// default was treated as "not really set"); post-fix
			// Changed() returns true because the operator did type
			// the flag in argv, so the conflict fatal MUST fire.
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1920x1080",
				"-auto_bitrate_max_height", "1920",
				"-auto_bitrate_min_height", "480",
				"-i", "test://in", "test://out",
			},
			wantFatal: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotFatal := runParseFlagsCatchFatal(t, c.args)
			if gotFatal != c.wantFatal {
				t.Fatalf("fatal-fired = %v, want %v (args=%v)", gotFatal, c.wantFatal, c.args)
			}
		})
	}
}

// TestParseFlags_AutoBitRateResolution_BandedSingle pins single-occurrence
// banded form: one -auto_bitrate_resolution WxH:LO-HI must produce exactly
// one ladder row at that resolution with the named bitrate band, and the
// envelope must collapse to that band (no codec-default inheritance).
func TestParseFlags_AutoBitRateResolution_BandedSingle(t *testing.T) {
	args := []string{
		"ffstream",
		"-c:v", "libx264", "-b:v", "4000000",
		"-auto_bitrate", "true",
		"-auto_bitrate_resolution", "1280x720:2M-5M",
		"-i", "test://in", "test://out",
	}
	_, flags := parseFlags(args)
	require.NotNil(t, flags.AutoBitRate)
	require.Len(t, flags.AutoBitRate.ResolutionsAndBitRates, 1)
	e := flags.AutoBitRate.ResolutionsAndBitRates[0]
	require.Equal(t, uint32(1280), e.Width)
	require.Equal(t, uint32(720), e.Height)
	require.Equal(t, uint64(2_000_000), uint64(e.BitrateLow))
	require.Equal(t, uint64(5_000_000), uint64(e.BitrateHigh))
	require.Equal(t, uint64(2_000_000), uint64(flags.AutoBitRate.MinBitRate))
	require.Equal(t, uint64(5_000_000), uint64(flags.AutoBitRate.MaxBitRate))
}

// TestParseFlags_AutoBitRateResolution_BandedMulti verifies the repeatable
// banded ladder: 3 occurrences must produce 3 rows sorted by BitrateLow,
// MinBitRate==smallest LO, MaxBitRate==largest HI. Args are passed in
// non-sorted order to exercise the sort path.
func TestParseFlags_AutoBitRateResolution_BandedMulti(t *testing.T) {
	args := []string{
		"ffstream",
		"-c:v", "libx264", "-b:v", "4000000",
		"-auto_bitrate", "true",
		"-auto_bitrate_resolution", "1920x1080:6M-9M",
		"-auto_bitrate_resolution", "640x360:500k-1M",
		"-auto_bitrate_resolution", "1280x720:2M-5M",
		"-i", "test://in", "test://out",
	}
	_, flags := parseFlags(args)
	require.NotNil(t, flags.AutoBitRate)
	require.Len(t, flags.AutoBitRate.ResolutionsAndBitRates, 3)
	rows := flags.AutoBitRate.ResolutionsAndBitRates
	require.Equal(t, uint32(640), rows[0].Width)
	require.Equal(t, uint32(360), rows[0].Height)
	require.Equal(t, uint64(500_000), uint64(rows[0].BitrateLow))
	require.Equal(t, uint64(1_000_000), uint64(rows[0].BitrateHigh))
	require.Equal(t, uint32(1280), rows[1].Width)
	require.Equal(t, uint32(720), rows[1].Height)
	require.Equal(t, uint64(2_000_000), uint64(rows[1].BitrateLow))
	require.Equal(t, uint64(5_000_000), uint64(rows[1].BitrateHigh))
	require.Equal(t, uint32(1920), rows[2].Width)
	require.Equal(t, uint32(1080), rows[2].Height)
	require.Equal(t, uint64(6_000_000), uint64(rows[2].BitrateLow))
	require.Equal(t, uint64(9_000_000), uint64(rows[2].BitrateHigh))
	require.Equal(t, uint64(500_000), uint64(flags.AutoBitRate.MinBitRate))
	require.Equal(t, uint64(9_000_000), uint64(flags.AutoBitRate.MaxBitRate))
}

// TestParseFlags_AutoBitRateResolution_OverlapFatal pins the overlap
// detector. Dual-sided: the equivalent non-overlapping ladder must succeed,
// proving the rule fires only on real overlaps rather than every
// multi-band invocation.
func TestParseFlags_AutoBitRateResolution_OverlapFatal(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantFatal bool
	}{
		{
			name: "overlap_fatal",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1280x720:1M-5M",
				"-auto_bitrate_resolution", "1920x1080:4M-9M",
				"-i", "test://in", "test://out",
			},
			wantFatal: true,
		},
		{
			name: "adjacent_ok",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1280x720:1M-5M",
				"-auto_bitrate_resolution", "1920x1080:6M-9M",
				"-i", "test://in", "test://out",
			},
			wantFatal: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotFatal := runParseFlagsCatchFatal(t, c.args)
			if gotFatal != c.wantFatal {
				t.Fatalf("fatal-fired = %v, want %v (args=%v)", gotFatal, c.wantFatal, c.args)
			}
		})
	}
}

// TestParseFlags_AutoBitRateResolution_BareRepeatedFatal pins the
// "bare form is not repeatable" rule. Dual-sided: a single bare
// occurrence must succeed.
func TestParseFlags_AutoBitRateResolution_BareRepeatedFatal(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantFatal bool
	}{
		{
			name: "bareRepeated_fatal",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1280x720",
				"-auto_bitrate_resolution", "1920x1080",
				"-i", "test://in", "test://out",
			},
			wantFatal: true,
		},
		{
			name: "bareSingle_ok",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1920x1080",
				"-i", "test://in", "test://out",
			},
			wantFatal: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotFatal := runParseFlagsCatchFatal(t, c.args)
			if gotFatal != c.wantFatal {
				t.Fatalf("fatal-fired = %v, want %v (args=%v)", gotFatal, c.wantFatal, c.args)
			}
		})
	}
}

// TestParseFlags_AutoBitRateResolution_BareAndBandedFatal pins the
// "cannot mix bare with banded" rule. Dual-sided: the all-banded
// equivalent must succeed.
func TestParseFlags_AutoBitRateResolution_BareAndBandedFatal(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantFatal bool
	}{
		{
			name: "bareAndBanded_fatal",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1280x720",
				"-auto_bitrate_resolution", "1920x1080:5M-9M",
				"-i", "test://in", "test://out",
			},
			wantFatal: true,
		},
		{
			name: "allBanded_ok",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1280x720:1M-5M",
				"-auto_bitrate_resolution", "1920x1080:6M-9M",
				"-i", "test://in", "test://out",
			},
			wantFatal: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotFatal := runParseFlagsCatchFatal(t, c.args)
			if gotFatal != c.wantFatal {
				t.Fatalf("fatal-fired = %v, want %v (args=%v)", gotFatal, c.wantFatal, c.args)
			}
		})
	}
}

// TestParseFlags_AutoBitRateResolution_LoGEHiFatal pins the LO<HI
// invariant: reversed (5M-2M) and equal (5M-5M) must both abort. Dual-
// sided: the canonical LO<HI form must succeed, proving the check
// targets degenerate ranges only.
func TestParseFlags_AutoBitRateResolution_LoGEHiFatal(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantFatal bool
	}{
		{
			name: "reversed_fatal",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1280x720:5M-2M",
				"-i", "test://in", "test://out",
			},
			wantFatal: true,
		},
		{
			name: "equal_fatal",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1280x720:5M-5M",
				"-i", "test://in", "test://out",
			},
			wantFatal: true,
		},
		{
			name: "canonical_ok",
			args: []string{
				"ffstream",
				"-c:v", "libx264", "-b:v", "4000000",
				"-auto_bitrate", "true",
				"-auto_bitrate_resolution", "1280x720:2M-5M",
				"-i", "test://in", "test://out",
			},
			wantFatal: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotFatal := runParseFlagsCatchFatal(t, c.args)
			if gotFatal != c.wantFatal {
				t.Fatalf("fatal-fired = %v, want %v (args=%v)", gotFatal, c.wantFatal, c.args)
			}
		})
	}
}

// TestParseAutoBitrateResolutionRows_Bare_BackwardCompat pins the bare-form
// contract: a single bare WxH must yield exactly one row with bareForm=true,
// and BitrateLow/BitrateHigh left at zero so the caller can fill in the
// codec-default envelope. Falsification: removing the bareForm return and
// branching on the zero-sentinel in flag.go would still satisfy this assertion
// directly, but the bareForm bool is what allows the caller to disambiguate
// from a (theoretically) banded row whose LO happens to be 0.
func TestParseAutoBitrateResolutionRows_Bare_BackwardCompat(t *testing.T) {
	rows, bareForm, err := parseAutoBitrateResolutionRows([]string{"1920x1080"})
	require.NoError(t, err)
	require.True(t, bareForm, "single bare WxH must set bareForm=true")
	require.Len(t, rows, 1)
	require.Equal(t, uint32(1920), rows[0].Width)
	require.Equal(t, uint32(1080), rows[0].Height)
	require.Zero(t, uint64(rows[0].BitrateLow), "bare row must have BitrateLow=0 for caller to fill envelope")
	require.Zero(t, uint64(rows[0].BitrateHigh), "bare row must have BitrateHigh=0 for caller to fill envelope")
}

// TestParseAutoBitrateResolutionRows_BandedSorting pins the sort contract:
// out-of-order banded inputs must be returned sorted by BitrateLow ascending
// and bareForm must be false. Falsification: commenting out the sort.Slice
// call leaves rows in input order and breaks the first assertion.
func TestParseAutoBitrateResolutionRows_BandedSorting(t *testing.T) {
	rows, bareForm, err := parseAutoBitrateResolutionRows([]string{
		"1920x1080:6M-9M",
		"640x360:500k-1M",
		"1280x720:2M-5999999",
	})
	require.NoError(t, err)
	require.False(t, bareForm, "all-banded input must set bareForm=false")
	require.Len(t, rows, 3)
	require.Equal(t, uint64(500_000), uint64(rows[0].BitrateLow))
	require.Equal(t, uint64(2_000_000), uint64(rows[1].BitrateLow))
	require.Equal(t, uint64(6_000_000), uint64(rows[2].BitrateLow))
}

// TestParseAutoBitrateResolutionRows_TouchingFatal pins the strict
// no-touching rule: adjacent ranges sharing an endpoint (BitRate() lookup
// is inclusive on both ends) must error. Dual-sided: the equivalent
// non-touching ladder must succeed. Falsification: changing the overlap
// check from `<=` to `<` would let "2M-5M"+"5M-12M" through and fail the
// touching_fatal subcase.
func TestParseAutoBitrateResolutionRows_TouchingFatal(t *testing.T) {
	cases := []struct {
		name    string
		vs      []string
		wantErr bool
	}{
		{
			name:    "touching_fatal",
			vs:      []string{"1280x720:2M-5M", "1920x1080:5M-12M"},
			wantErr: true,
		},
		{
			name:    "nonTouching_ok",
			vs:      []string{"1280x720:2M-4999999", "1920x1080:5M-12M"},
			wantErr: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := parseAutoBitrateResolutionRows(c.vs)
			if c.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// runParseFlagsCatchFatal invokes parseFlags and returns true iff the
// project-fatal path raised the testFatalPanic sentinel installed by
// init(). Non-sentinel panics re-propagate so they surface as ordinary
// test failures with their original stack.
func runParseFlagsCatchFatal(t *testing.T, args []string) (gotFatal bool) {
	t.Helper()
	defer func() {
		r := recover()
		switch r.(type) {
		case nil:
			return
		case testFatalPanic:
			gotFatal = true
		default:
			panic(r)
		}
	}()
	_, _ = parseFlags(args)
	return false
}

func TestExtractAndStripPriority(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name         string
		in           avptypes.DictionaryItems
		wantPriority uint
		wantOpts     avptypes.DictionaryItems
	}{
		{
			name:         "empty",
			in:           nil,
			wantPriority: 0,
			wantOpts:     nil,
		},
		{
			name: "noPriorityKey",
			in: avptypes.DictionaryItems{
				{Key: "foo", Value: "bar"},
			},
			wantPriority: 0,
			wantOpts: avptypes.DictionaryItems{
				{Key: "foo", Value: "bar"},
			},
		},
		{
			name: "singlePriorityOnly",
			// fallback_priority is the only entry; after stripping the
			// result slice is empty so the implementation returns nil.
			in: avptypes.DictionaryItems{
				{Key: "fallback_priority", Value: "5"},
			},
			wantPriority: 5,
			wantOpts:     nil,
		},
		{
			name: "mixed",
			in: avptypes.DictionaryItems{
				{Key: "foo", Value: "bar"},
				{Key: "fallback_priority", Value: "3"},
				{Key: "baz", Value: "qux"},
			},
			wantPriority: 3,
			wantOpts: avptypes.DictionaryItems{
				{Key: "foo", Value: "bar"},
				{Key: "baz", Value: "qux"},
			},
		},
		{
			name: "malformedValue",
			// On parse failure the implementation logs and `continue`s,
			// which means the bad entry is stripped (not preserved) and
			// priority stays at 0. With no other items the result is nil.
			in: avptypes.DictionaryItems{
				{Key: "fallback_priority", Value: "not-a-number"},
			},
			wantPriority: 0,
			wantOpts:     nil,
		},
		{
			name: "duplicatePriorityLastWins",
			// Loop overwrites priority on each match → last value wins.
			// Both entries are stripped from the result.
			in: avptypes.DictionaryItems{
				{Key: "fallback_priority", Value: "2"},
				{Key: "fallback_priority", Value: "7"},
			},
			wantPriority: 7,
			wantOpts:     nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPriority, gotOpts := extractAndStripPriority(ctx, tc.in)
			if gotPriority != tc.wantPriority {
				t.Fatalf("priority: want %d, got %d", tc.wantPriority, gotPriority)
			}
			if !reflect.DeepEqual(gotOpts, tc.wantOpts) {
				t.Fatalf("opts:\nwant: %#v\n got: %#v", tc.wantOpts, gotOpts)
			}
		})
	}
}
