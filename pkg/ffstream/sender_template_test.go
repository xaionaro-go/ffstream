//go:build with_libav

package ffstream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	audio "github.com/xaionaro-go/audio/pkg/audio/types"
	"github.com/xaionaro-go/avpipeline/codec"
	codectypes "github.com/xaionaro-go/avpipeline/codec/types"
	streammux "github.com/xaionaro-go/avpipeline/preset/streammux"
)

func TestSenderTemplate_GetURL(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		template string
		key      streammux.SenderKey
		want     string
	}{
		{
			name:     "all variables substituted",
			template: "rtmp://server/${v:0:codec}/${v:0:width}x${v:0:height}/${a:0:codec}/${a:0:rate}",
			key: streammux.SenderKey{
				VideoCodec:      codectypes.Name("libx264"),
				VideoResolution: codec.Resolution{Width: 1920, Height: 1080},
				AudioCodec:      codectypes.Name("aac"),
				AudioSampleRate: audio.SampleRate(48000),
			},
			want: "rtmp://server/h264/1920x1080/aac/48000",
		},
		{
			name:     "no template variables",
			template: "rtmp://server/stream",
			key: streammux.SenderKey{
				VideoCodec: codectypes.Name("libx264"),
			},
			want: "rtmp://server/stream",
		},
		{
			name:     "zero values produce empty substitutions",
			template: "rtmp://server/${v:0:width}x${v:0:height}/${a:0:rate}",
			key: streammux.SenderKey{
				VideoCodec:      codectypes.Name("libx264"),
				VideoResolution: codec.Resolution{},
				AudioSampleRate: 0,
			},
			want: "rtmp://server/x/",
		},
		{
			name:     "only audio variables",
			template: "rtmp://server/${a:0:codec}/${a:0:rate}",
			key: streammux.SenderKey{
				AudioCodec:      codectypes.Name("aac"),
				AudioSampleRate: audio.SampleRate(44100),
			},
			want: "rtmp://server/aac/44100",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl := &SenderTemplate{URLTemplate: tt.template}
			got := tmpl.GetURL(ctx, tt.key)
			assert.Equal(t, tt.want, got)
		})
	}
}
