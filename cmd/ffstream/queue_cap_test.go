// queue_cap_test.go covers the framerate-adaptive default transcoder
// queue cap logic. The cap formula is documented and motivated by the F2
// reconfig pause measurement: at 60 fps the historical 60-frame default
// gave 0% headroom against the ~1 s upper-bound MediaCodec encoder
// reconfig pause. See /tmp/mission_f2_measure.md.
package main

import (
	"math"
	"testing"
)

// TestComputeTranscoderInputCap_AdaptiveFromFramerate pins the
// max(60, ceil(fps*2)) formula across the framerates that production has
// historically targeted.
func TestComputeTranscoderInputCap_AdaptiveFromFramerate(t *testing.T) {
	cases := []struct {
		name string
		fps  float64
		want uint64
	}{
		// Below the 60-frame floor: 25 fps * 2 = 50, clamped up to 60.
		{name: "fps25_clampToFloor", fps: 25, want: 60},
		// 30 fps * 2 = 60 — exactly at the floor.
		{name: "fps30_atFloor", fps: 30, want: 60},
		// 50 fps * 2 = 100 — above the floor.
		{name: "fps50_aboveFloor", fps: 50, want: 100},
		// 60 fps * 2 = 120 — F2's marginal case at the historical default.
		{name: "fps60_doubleDefault", fps: 60, want: 120},
		// Fractional input rounds up via ceil so the headroom contract
		// (>= fps*2 frames) holds for any positive rate. 29.97 * 2 =
		// 59.94 → ceil = 60, still hits the floor.
		{name: "fps29_97_ceil", fps: 29.97, want: 60},
		// 59.94 * 2 = 119.88 → ceil = 120.
		{name: "fps59_94_ceil", fps: 59.94, want: 120},
		// 120 fps * 2 = 240 — high-FPS hypothetical.
		{name: "fps120_highRate", fps: 120, want: 240},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeTranscoderInputCap(0, tc.fps)
			if got != tc.want {
				t.Errorf("computeTranscoderInputCap(0, %v) = %d, want %d", tc.fps, got, tc.want)
			}
		})
	}
}

// TestComputeTranscoderInputCap_FramerateUnsetFallsBackToSentinel pins
// the no-framerate fallback: the sentinel 0 must propagate so the
// downstream SetDefaultQueueSizes call leaves the avpipeline compiled-in
// default (60) untouched. Any non-zero return when both inputs are zero
// would constitute the cap silently disagreeing with the documented
// "sentinel 0 = leave compiled-in default" contract.
func TestComputeTranscoderInputCap_FramerateUnsetFallsBackToSentinel(t *testing.T) {
	got := computeTranscoderInputCap(0, 0)
	if got != 0 {
		t.Errorf("computeTranscoderInputCap(0, 0) = %d, want 0 (sentinel: leave compiled-in default)", got)
	}
}

// TestComputeTranscoderInputCap_NegativeFramerateTreatedAsUnset guards
// against bogus inputs (parser anomaly, negative -r value): they must
// not produce a junk cap. Treat as unset, fall back to sentinel.
func TestComputeTranscoderInputCap_NegativeFramerateTreatedAsUnset(t *testing.T) {
	got := computeTranscoderInputCap(0, -10)
	if got != 0 {
		t.Errorf("computeTranscoderInputCap(0, -10) = %d, want 0 (negative fps treated as unset)", got)
	}
}

// TestParseFlags_FramerateLandsInFlagsField checks the wiring between
// the parser and the cap helper: -r must populate Flags.Framerate so
// main.go can feed it into computeTranscoderInputCap. Without this,
// parseFlags would silently drop -r and the adaptive cap would never
// activate even when the operator passed a framerate.
func TestParseFlags_FramerateLandsInFlagsField(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want float64
	}{
		{
			name: "unsetDefaultsToZero",
			args: []string{"ffstream", "-i", "rtsp://input1", "rtmp://output"},
			want: 0,
		},
		{
			name: "integerFps",
			args: []string{"ffstream", "-i", "rtsp://input1", "-r", "30", "rtmp://output"},
			want: 30,
		},
		{
			name: "fractionalFps",
			args: []string{"ffstream", "-i", "rtsp://input1", "-r", "29.97", "rtmp://output"},
			want: 29.97,
		},
		{
			name: "highFps",
			args: []string{"ffstream", "-i", "rtsp://input1", "-r", "60", "rtmp://output"},
			want: 60,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, flags := parseFlags(tc.args)
			if math.Abs(flags.Framerate-tc.want) > 1e-9 {
				t.Errorf("Flags.Framerate = %v, want %v", flags.Framerate, tc.want)
			}
		})
	}
}

// TestComputeTranscoderInputCap_ExplicitOverrideWins pins precedence: a
// user-supplied -queue_size_transcoder N must override the adaptive
// default regardless of -r. This is the documented escape hatch and the
// only way to set the cap below the framerate-adaptive floor for
// experimentation.
func TestComputeTranscoderInputCap_ExplicitOverrideWins(t *testing.T) {
	cases := []struct {
		name     string
		override uint64
		fps      float64
	}{
		{name: "lowOverride_at60fps", override: 80, fps: 60},
		{name: "highOverride_at30fps", override: 500, fps: 30},
		{name: "overrideEqualsFloor_noFps", override: 60, fps: 0},
		{name: "overrideBelowFloor_at60fps", override: 30, fps: 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeTranscoderInputCap(tc.override, tc.fps)
			if got != tc.override {
				t.Errorf("computeTranscoderInputCap(%d, %v) = %d, want %d (override must win)",
					tc.override, tc.fps, got, tc.override)
			}
		})
	}
}
