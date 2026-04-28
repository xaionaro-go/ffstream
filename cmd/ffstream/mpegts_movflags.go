// mpegts_movflags.go injects fragmented-MP4 movflags when the output
// format is mpegts. The CLI parser strips leading dashes, so the format
// option is stored under key "f" (not "-f").
package main

import (
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

const mpegtsFragmentedMovflags = "frag_keyframe+empty_moov+separate_moof"

func injectMpegtsMovflags(opts avptypes.DictionaryItems) avptypes.DictionaryItems {
	var outputFormat string
	for _, v := range opts {
		if v.Key == "f" {
			outputFormat = v.Value
			break
		}
	}
	if outputFormat != "mpegts" {
		return opts
	}

	var movFlags *avptypes.DictionaryItem
	for idx, item := range opts {
		if item.Key == "movflags" {
			movFlags = &opts[idx]
			break
		}
	}
	if movFlags == nil {
		opts = append(opts, avptypes.DictionaryItem{Key: "movflags"})
		movFlags = &opts[len(opts)-1]
	}
	if movFlags.Value != "" {
		movFlags.Value += "+"
	}
	movFlags.Value += mpegtsFragmentedMovflags
	return opts
}
