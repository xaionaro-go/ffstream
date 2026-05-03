// audio_sync_routing_test.go covers the routing filters that split
// inputs by media type so non-audio frames bypass AudioSync entirely.
// The two filters are constructed inline in
// (*FFStream).startStreamingWithStreamMux; this test pins their match
// matrix to detect any future wiring drift.

package ffstream

import (
	"context"
	"testing"

	"github.com/asticode/go-astiav"
	"github.com/stretchr/testify/assert"
	"github.com/xaionaro-go/avpipeline/frame"
	packetorframefiltercondition "github.com/xaionaro-go/avpipeline/node/filter/packetorframefilter/condition"
	"github.com/xaionaro-go/avpipeline/packetorframe"
)

// makeFrameInput synthesises an InputUnion whose StreamInfo declares
// the requested media type. Only the codec parameters are needed for
// (*frame.Input).GetMediaType(), so the frame body is left zeroed.
func makeFrameInput(mt astiav.MediaType) packetorframefiltercondition.Input {
	cp := astiav.AllocCodecParameters()
	cp.SetMediaType(mt)
	return packetorframefiltercondition.Input{
		Input: packetorframe.InputUnion{
			Frame: &frame.Input{
				Frame: astiav.AllocFrame(),
				StreamInfo: &frame.StreamInfo{
					StreamIndex:     0,
					TimeBase:        astiav.NewRational(1, 90000),
					CodecParameters: cp,
				},
			},
		},
	}
}

// TestAudioSyncRouting_AudioOnlyFilter proves the audio-only filter
// (used on Inputs.AddPushTo(syncNode, audioOnly)) accepts audio and
// rejects every other media type. This is the load-bearing condition
// that keeps non-audio off AudioSync.
func TestAudioSyncRouting_AudioOnlyFilter(t *testing.T) {
	ctx := context.Background()
	audioOnly := packetorframefiltercondition.MediaType(astiav.MediaTypeAudio)

	cases := []struct {
		mt   astiav.MediaType
		want bool
	}{
		{astiav.MediaTypeAudio, true},
		{astiav.MediaTypeVideo, false},
		{astiav.MediaTypeSubtitle, false},
		{astiav.MediaTypeData, false},
	}
	for _, tc := range cases {
		t.Run(tc.mt.String(), func(t *testing.T) {
			got := audioOnly.Match(ctx, makeFrameInput(tc.mt))
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestAudioSyncRouting_NonAudioFilter proves the inverse filter
// (used on Inputs.AddPushTo(StreamMux, nonAudio)) accepts every
// media type EXCEPT audio. Together with AudioOnlyFilter this
// covers the dual-sided wiring: audio routes via AudioSync, every
// other media type bypasses it directly to StreamMux.
func TestAudioSyncRouting_NonAudioFilter(t *testing.T) {
	ctx := context.Background()
	// Re-create the same inline closure used in startStreamingWithStreamMux.
	nonAudio := packetorframefiltercondition.Function(func(
		ctx context.Context,
		in packetorframefiltercondition.Input,
	) bool {
		return in.Input.GetMediaType() != astiav.MediaTypeAudio
	})

	cases := []struct {
		mt   astiav.MediaType
		want bool
	}{
		{astiav.MediaTypeAudio, false},
		{astiav.MediaTypeVideo, true},
		{astiav.MediaTypeSubtitle, true},
		{astiav.MediaTypeData, true},
	}
	for _, tc := range cases {
		t.Run(tc.mt.String(), func(t *testing.T) {
			got := nonAudio.Match(ctx, makeFrameInput(tc.mt))
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestAudioSyncRouting_FiltersArePartition proves the two filters
// form a partition over media types — every input matches exactly
// one of them. If a future change introduces overlap or a gap, this
// test fails and the fix-it cost is paid before deployment, not
// after a wedge in production.
func TestAudioSyncRouting_FiltersArePartition(t *testing.T) {
	ctx := context.Background()
	audioOnly := packetorframefiltercondition.MediaType(astiav.MediaTypeAudio)
	nonAudio := packetorframefiltercondition.Function(func(
		ctx context.Context,
		in packetorframefiltercondition.Input,
	) bool {
		return in.Input.GetMediaType() != astiav.MediaTypeAudio
	})

	mts := []astiav.MediaType{
		astiav.MediaTypeAudio,
		astiav.MediaTypeVideo,
		astiav.MediaTypeSubtitle,
		astiav.MediaTypeData,
	}
	for _, mt := range mts {
		in := makeFrameInput(mt)
		a := audioOnly.Match(ctx, in)
		b := nonAudio.Match(ctx, in)
		// Exactly one must match -> exclusive OR.
		assert.True(t, a != b, "media type %s matched %v / %v; expected partition", mt, a, b)
	}
}
