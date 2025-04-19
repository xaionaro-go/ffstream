package main

import (
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream/types"
)

func convertUnknownOptionsToCustomOptions(
	unknownOpts []string,
) types.DictionaryItems {
	var result types.DictionaryItems

	for idx := 0; idx < len(unknownOpts)-1; idx += 2 {
		arg := unknownOpts[idx]

		opt := arg
		value := unknownOpts[idx+1]

		result = append(result, types.DictionaryItem{
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
