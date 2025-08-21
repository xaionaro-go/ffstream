package main

import (
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

func convertUnknownOptionsToCustomOptions(
	unknownOpts []string,
) streammuxtypes.DictionaryItems {
	var result streammuxtypes.DictionaryItems

	for idx := 0; idx < len(unknownOpts)-1; idx += 2 {
		arg := unknownOpts[idx]

		opt := arg
		value := unknownOpts[idx+1]

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

	for idx := 0; idx < len(unknownOpts)-1; idx += 2 {
		arg := unknownOpts[idx]

		opt := arg
		value := unknownOpts[idx+1]

		result = append(result, avptypes.DictionaryItem{
			Key:   opt,
			Value: value,
		})
	}

	return result
}
