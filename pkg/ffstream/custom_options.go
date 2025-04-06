package ffstream

import (
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream/types"
)

func convertCustomOptions(
	opts types.DictionaryItems,
) avptypes.DictionaryItems {
	r := make(avptypes.DictionaryItems, 0, len(opts))
	for _, v := range opts {
		r = append(r, avptypes.DictionaryItem{
			Key:   v.Key,
			Value: v.Value,
		})
	}
	return r
}
