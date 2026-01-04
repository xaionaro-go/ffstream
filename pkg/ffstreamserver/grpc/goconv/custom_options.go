// custom_options.go provides conversion functions for custom options between Go and GRPC types.

package goconv

import (
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
)

func CustomOptionsFromGRPC(
	opts []*avpipeline.CustomOption,
) []streammuxtypes.DictionaryItem {
	result := make([]streammuxtypes.DictionaryItem, 0, len(opts))

	for _, opt := range opts {
		result = append(result, streammuxtypes.DictionaryItem{
			Key:   opt.GetKey(),
			Value: opt.GetValue(),
		})
	}

	return result
}

func CustomOptionsToGRPC(
	opts []streammuxtypes.DictionaryItem,
) []*avpipeline.CustomOption {
	result := make([]*avpipeline.CustomOption, 0, len(opts))

	for _, opt := range opts {
		result = append(result, &avpipeline.CustomOption{
			Key:   opt.Key,
			Value: opt.Value,
		})
	}

	return result
}
