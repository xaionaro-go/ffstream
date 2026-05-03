// queue_cap.go derives the framerate-adaptive default for the
// transcoder input queue cap. F2 measurement
// (/tmp/mission_f2_measure.md) established that the historical 60-frame
// avpipeline default leaves 0% headroom against the worst-case
// MediaCodec encoder reconfig pause (~1 s upper bound) at 60 fps. The
// adaptive formula max(60, ceil(fps*2)) keeps the existing 30 fps
// production budget unchanged (60 frames = 2 s tolerance) and
// auto-doubles the cap for higher rates.

package main

import "math"

// transcoderInputCapFloor is the historical avpipeline compiled-in
// default. Adaptive scaling never drops below this so 30 fps and below
// continue to behave exactly as before this change.
const transcoderInputCapFloor uint64 = 60

// computeTranscoderInputCap returns the value to pass as the
// transcoder-input parameter of processor.SetDefaultQueueSizes.
//
// Inputs:
//   - override: user-supplied -queue_size_transcoder. Any non-zero value
//     wins outright — the operator's explicit cap must never be silently
//     overridden by adaptive logic.
//   - fps: framerate from -r. Zero or negative means unset; in that case
//     the function returns the sentinel 0, which downstream
//     SetDefaultQueueSizes interprets as "leave the compiled-in default
//     unchanged" (60 frames). This preserves pre-F2 behaviour for users
//     who do not pass -r.
//
// Otherwise the result is max(transcoderInputCapFloor, ceil(fps*2)).
// The ceil ensures fractional rates (e.g. 29.97, 59.94) still get at
// least fps*2 frames of buffering, satisfying the "2 s tolerance against
// reconfig pause" budget the floor was originally sized for at 30 fps.
func computeTranscoderInputCap(override uint64, fps float64) uint64 {
	if override != 0 {
		return override
	}
	if fps <= 0 {
		return 0
	}
	adaptive := uint64(math.Ceil(fps * 2))
	if adaptive < transcoderInputCapFloor {
		return transcoderInputCapFloor
	}
	return adaptive
}
