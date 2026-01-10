// unknown_options.go provides functions to handle unknown command-line options.
package main

import (
	"strings"

	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

func convertUnknownOptionsToCustomOptions(
	unknownOpts []string,
) streammuxtypes.DictionaryItems {
	var result streammuxtypes.DictionaryItems

	for idx := 0; idx < len(unknownOpts); idx++ {
		arg := unknownOpts[idx]
		if !strings.HasPrefix(arg, "-") {
			continue
		}

		opt := strings.TrimPrefix(arg, "-")
		var value string
		if idx+1 < len(unknownOpts) && !strings.HasPrefix(unknownOpts[idx+1], "-") {
			value = unknownOpts[idx+1]
			idx++
		}

		result = append(result, streammuxtypes.DictionaryItem{
			Key:   opt,
			Value: value,
		})
	}

	return result
}

func convertUnknownOptionsToAVPCustomOptions(
	unknownOpts []string,
) avptypes.DictionaryItems {
	var result avptypes.DictionaryItems

	for idx := 0; idx < len(unknownOpts); idx++ {
		arg := unknownOpts[idx]
		if !strings.HasPrefix(arg, "-") {
			continue
		}

		opt := strings.TrimPrefix(arg, "-")
		var value string
		if idx+1 < len(unknownOpts) && !strings.HasPrefix(unknownOpts[idx+1], "-") {
			value = unknownOpts[idx+1]
			idx++
		}

		result = append(result, avptypes.DictionaryItem{
			Key:   opt,
			Value: value,
		})
	}

	return result
}
