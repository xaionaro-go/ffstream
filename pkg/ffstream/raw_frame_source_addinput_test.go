// raw_frame_source_addinput_test.go pins the AddInput → SetRawFrameSource
// trigger path on the live StreamMux. The production sequence is:
//
//   1. ffstream.Start runs with only the URL fallback resource (rtmp).
//   2. wingout's gRPC AddInput hot-adds android_camera at priority 0
//      AFTER Start has wired StreamMux but BEFORE the encoder has been
//      opened against the first frame.
//   3. The hot-add path inside (*FFStream).AddInput must latch
//      StreamMux.RawFrameSource so the lazily-opened MediaCodec encoder
//      takes the SW pix_fmt branch instead of the silent-consume Surface
//      passthrough.
//
// The original finding is that ffstream.Start later called
// SetRawFrameSource(s.hasRawFrameSourceInput()), which clobbered the
// hot-add latch in some boot orderings. The fix is to call
// SetRawFrameSource only on the true arm; the avpipeline OneWayBool
// type is the second line of defence, but caller-side discipline
// avoids logging spam and documents intent.

package ffstream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/kernel/extra/android"
	"github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

// newFFStreamWithStreamMuxStub returns an FFStream with a StreamMux
// wired in but without running Start. Sufficient to exercise the
// AddInput → SetRawFrameSource trigger guard inside (*FFStream).AddInput.
func newFFStreamWithStreamMuxStub(t *testing.T, ctx context.Context) *FFStream {
	t.Helper()
	s, err := New(ctx)
	require.NoError(t, err)
	s.StreamMux, err = streammux.NewWithCustomData[CustomData](
		ctx, streammuxtypes.MuxModeSameOutputSameTracks, nil)
	require.NoError(t, err)
	return s
}

// TestAddInput_RawFrameSourceFormat_LatchesStreamMux is the GOOD-side
// of the hot-add trigger: AddInput with android_camera flips
// StreamMux.RawFrameSource on. Without this, the encoder pix_fmt fix
// would never engage on the production layout that adds the camera
// AFTER Start.
func TestAddInput_RawFrameSourceFormat_LatchesStreamMux(t *testing.T) {
	ctx := context.Background()
	s := newFFStreamWithStreamMuxStub(t, ctx)
	require.False(t, s.StreamMux.RawFrameSource.Load(), "sanity: not yet latched")

	res := Resource{
		URL:      "0",
		Priority: 0,
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "f", Value: android.InputFormat},
			},
		},
	}
	_, err := s.AddInput(ctx, res)
	require.NoError(t, err)
	assert.True(t, s.StreamMux.RawFrameSource.Load(),
		"AddInput with android_camera must latch StreamMux.RawFrameSource")
}

// TestAddInput_RawFrameSourceMicrophone_LatchesStreamMux is the
// audio-side counterpart of the GOOD test. wingout's hot-add adds both
// camera and microphone; either alone must latch the flag.
func TestAddInput_RawFrameSourceMicrophone_LatchesStreamMux(t *testing.T) {
	ctx := context.Background()
	s := newFFStreamWithStreamMuxStub(t, ctx)

	res := Resource{
		URL:      "default",
		Priority: 0,
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "f", Value: android.MicrophoneInputFormat},
			},
		},
	}
	_, err := s.AddInput(ctx, res)
	require.NoError(t, err)
	assert.True(t, s.StreamMux.RawFrameSource.Load(),
		"AddInput with android_microphone must latch StreamMux.RawFrameSource")
}

// TestAddInput_NonRawFrameFormat_DoesNotLatch is the BAD-side: an RTMP
// or other URL/file input must NOT latch the flag, otherwise existing
// transcoding-from-decoder pipelines would be silently rerouted onto
// the SW-upload path.
func TestAddInput_NonRawFrameFormat_DoesNotLatch(t *testing.T) {
	ctx := context.Background()
	s := newFFStreamWithStreamMuxStub(t, ctx)

	res := Resource{
		URL:      "rtmp://example/live",
		Priority: 0,
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "f", Value: "flv"},
			},
		},
	}
	_, err := s.AddInput(ctx, res)
	require.NoError(t, err)
	assert.False(t, s.StreamMux.RawFrameSource.Load(),
		"AddInput with non-raw-frame format must NOT latch StreamMux.RawFrameSource")
}

// TestAddInput_RawFrameThenNonRaw_StaysLatched pins the sticky-true
// contract from the AddInput entry point: once a camera hot-add has
// latched the flag, a subsequent fallback AddInput must not clear it.
// The avpipeline-side OneWayBool guarantees this at the type level;
// this test pins it from the ffstream caller's vantage so a future
// change to the AddInput control flow cannot regress it silently.
func TestAddInput_RawFrameThenNonRaw_StaysLatched(t *testing.T) {
	ctx := context.Background()
	s := newFFStreamWithStreamMuxStub(t, ctx)

	camera := Resource{
		URL:      "0",
		Priority: 0,
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "f", Value: android.InputFormat},
			},
		},
	}
	_, err := s.AddInput(ctx, camera)
	require.NoError(t, err)
	require.True(t, s.StreamMux.RawFrameSource.Load(), "sanity: latched by camera")

	rtmp := Resource{
		URL:      "rtmp://example/live",
		Priority: 1,
		InputConfig: kernel.InputConfig{
			CustomOptions: avptypes.DictionaryItems{
				{Key: "f", Value: "flv"},
			},
		},
	}
	_, err = s.AddInput(ctx, rtmp)
	require.NoError(t, err)
	assert.True(t, s.StreamMux.RawFrameSource.Load(),
		"sticky-true: subsequent non-raw AddInput must NOT clear the latch")
}
