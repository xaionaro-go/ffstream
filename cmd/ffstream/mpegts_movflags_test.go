// mpegts_movflags_test.go contains tests for injectMpegtsMovflags.
package main

import (
	"reflect"
	"testing"

	avptypes "github.com/xaionaro-go/avpipeline/types"
)

func TestInjectMpegtsMovflags(t *testing.T) {
	const fragFlags = "frag_keyframe+empty_moov+separate_moof"

	cases := []struct {
		name string
		in   avptypes.DictionaryItems
		want avptypes.DictionaryItems
	}{
		{
			name: "noFormat",
			in: avptypes.DictionaryItems{
				{Key: "b:v", Value: "5M"},
			},
			want: avptypes.DictionaryItems{
				{Key: "b:v", Value: "5M"},
			},
		},
		{
			name: "nonMpegtsFormat",
			in: avptypes.DictionaryItems{
				{Key: "f", Value: "flv"},
			},
			want: avptypes.DictionaryItems{
				{Key: "f", Value: "flv"},
			},
		},
		{
			name: "mpegtsNoExistingMovflags",
			in: avptypes.DictionaryItems{
				{Key: "f", Value: "mpegts"},
			},
			want: avptypes.DictionaryItems{
				{Key: "f", Value: "mpegts"},
				{Key: "movflags", Value: fragFlags},
			},
		},
		{
			name: "mpegtsWithExistingMovflags",
			in: avptypes.DictionaryItems{
				{Key: "f", Value: "mpegts"},
				{Key: "movflags", Value: "faststart"},
			},
			want: avptypes.DictionaryItems{
				{Key: "f", Value: "mpegts"},
				{Key: "movflags", Value: "faststart+" + fragFlags},
			},
		},
		{
			name: "mpegtsWithEmptyMovflags",
			in: avptypes.DictionaryItems{
				{Key: "f", Value: "mpegts"},
				{Key: "movflags", Value: ""},
			},
			want: avptypes.DictionaryItems{
				{Key: "f", Value: "mpegts"},
				{Key: "movflags", Value: fragFlags},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := injectMpegtsMovflags(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("opts:\nwant: %#v\n got: %#v", tc.want, got)
			}
		})
	}
}
