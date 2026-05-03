// raw_frame_source_test.go pins the raw-frame source classifier and the
// FFStream.hasRawFrameSourceInput aggregation. This is the unit-level
// proof of the ffstream side of the silent-consume fix.

package ffstream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/kernel/extra/android"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

// TestIsRawFrameSourceFormat_KnownFormats pins the GOOD-side
// classification: every format that pushes decoded frames into streammux
// without a downstream decoder must be reported as raw-frame.
func TestIsRawFrameSourceFormat_KnownFormats(t *testing.T) {
	for _, f := range []string{
		android.InputFormat,           // android_camera
		android.MicrophoneInputFormat, // android_microphone
	} {
		assert.Truef(t, isRawFrameSourceFormat(f),
			"format %q must be classified as raw-frame source", f)
	}
}

// TestIsRawFrameSourceFormat_PacketSourceFormats pins the BAD-side: every
// format that flows through a kernel.Input -> Decoder chain (URL/file
// inputs, network protocols) must NOT be reported as raw-frame, otherwise
// existing transcoding-from-decoder pipelines would be silently rerouted
// onto the SW-upload path and lose the surface-passthrough optimisation.
func TestIsRawFrameSourceFormat_PacketSourceFormats(t *testing.T) {
	for _, f := range []string{
		"", "mpegts", "flv", "mp4", "rtmp", "rtsp", "srt", "v4l2", "lavfi",
	} {
		assert.Falsef(t, isRawFrameSourceFormat(f),
			"format %q must NOT be classified as raw-frame source", f)
	}
}

// TestFFStream_HasRawFrameSourceInput_NoInputs is the BAD-side: a fresh
// FFStream with no resources must report false, otherwise StreamMux would
// be misconfigured before the user has even called AddInput.
func TestFFStream_HasRawFrameSourceInput_NoInputs(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx)
	require.NoError(t, err)
	assert.False(t, s.hasRawFrameSourceInput(),
		"FFStream with no inputs must report hasRawFrameSourceInput=false")
}

// TestFFStream_HasRawFrameSourceInput_OnlyURL is the BAD-side: a pipeline
// configured with only URL/file inputs must keep the legacy behaviour and
// report false.
func TestFFStream_HasRawFrameSourceInput_OnlyURL(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx)
	require.NoError(t, err)
	s.InputsInfo = []Resources{{
		{URL: "rtmp://example/live", InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{{Key: "f", Value: "flv"}},
		}},
	}}
	assert.False(t, s.hasRawFrameSourceInput(),
		"URL-only inputs must report hasRawFrameSourceInput=false")
}

// TestFFStream_HasRawFrameSourceInput_Camera is the GOOD-side: a pipeline
// with android_camera at any priority must report true so streammux can
// arm the MediaCodec pix_fmt fix.
func TestFFStream_HasRawFrameSourceInput_Camera(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx)
	require.NoError(t, err)
	s.InputsInfo = []Resources{{
		{URL: "0", InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{{Key: "f", Value: android.InputFormat}},
		}},
	}}
	assert.True(t, s.hasRawFrameSourceInput(),
		"android_camera input must report hasRawFrameSourceInput=true")
}

// TestFFStream_HasRawFrameSourceInput_Microphone is the GOOD-side
// counterpart for the audio raw-frame source.
func TestFFStream_HasRawFrameSourceInput_Microphone(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx)
	require.NoError(t, err)
	s.InputsInfo = []Resources{{
		{URL: "default", InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{{Key: "f", Value: android.MicrophoneInputFormat}},
		}},
	}}
	assert.True(t, s.hasRawFrameSourceInput(),
		"android_microphone input must report hasRawFrameSourceInput=true")
}

// TestFFStream_HasRawFrameSourceInput_MixedFallback is the GOOD-side for
// the production layout: camera at priority 0 and an RTMP fallback at a
// higher priority. The flag must report true because the camera path is
// the one that drives MediaCodec encoder setup; otherwise the camera-only
// silent-consume bug regresses any time the operator adds a fallback.
func TestFFStream_HasRawFrameSourceInput_MixedFallback(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx)
	require.NoError(t, err)
	s.InputsInfo = []Resources{
		{
			{URL: "0", InputConfig: kernel.InputConfig{
				CustomOptions: avptypes.DictionaryItems{{Key: "f", Value: android.InputFormat}},
			}},
			{URL: "default", InputConfig: kernel.InputConfig{
				CustomOptions: avptypes.DictionaryItems{{Key: "f", Value: android.MicrophoneInputFormat}},
			}},
		},
		{
			{URL: "rtmp://0.0.0.0/live", InputConfig: kernel.InputConfig{
				CustomOptions: avptypes.DictionaryItems{{Key: "f", Value: "flv"}},
			}},
		},
	}
	assert.True(t, s.hasRawFrameSourceInput(),
		"camera + rtmp-fallback must report hasRawFrameSourceInput=true")
}
