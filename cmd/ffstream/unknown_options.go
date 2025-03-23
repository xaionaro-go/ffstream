package main

import (
	"github.com/xaionaro-go/avpipeline/types"
)

func convertUnknownOptionsToCustomOptions(
	unknownOpts []string,
) types.DictionaryItems {
	var result types.DictionaryItems

	for idx := 0; idx < len(unknownOpts)-1; idx++ {
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
