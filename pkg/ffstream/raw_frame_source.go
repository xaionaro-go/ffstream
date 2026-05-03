// raw_frame_source.go classifies input resources by whether they supply
// decoded frames directly to streammux (android_camera + android_microphone)
// rather than packets that need a downstream decoder. The result drives
// streammux's RawFrameSource flag which routes MediaCodec encoders away
// from the get_format -> AV_PIX_FMT_MEDIACODEC silent-consume trap.

package ffstream

import (
	"github.com/xaionaro-go/avpipeline/kernel/extra/android"
)

// isRawFrameSourceFormat reports whether the input format name describes a
// pipeline that pushes decoded frames directly into streammux (no
// downstream decoder). Currently the Android device demuxers
// (android_camera, android_microphone) are the only such formats; URL/file
// inputs all flow through a kernel.Input -> Decoder chain which produces
// frames via the normal get_format negotiation, so they keep RawFrameSource
// false.
func isRawFrameSourceFormat(formatName string) bool {
	switch formatName {
	case android.InputFormat, android.MicrophoneInputFormat:
		return true
	}
	return false
}

// hasRawFrameSourceInput reports whether any currently-configured input
// resource is a raw-frame source. It iterates s.InputsInfo without taking
// any locks: the caller (Start) holds s.locker and InputsInfo is only
// extended (not pruned) by AddInput, so a concurrent AddInput can only
// add never-before-seen entries — which would also need RawFrameSource
// support if added before Start completes, captured by the Start-time
// snapshot here.
func (s *FFStream) hasRawFrameSourceInput() bool {
	for _, resources := range s.InputsInfo {
		for _, res := range resources {
			if isRawFrameSourceFormat(inputFormatFromResource(res)) {
				return true
			}
		}
	}
	return false
}
