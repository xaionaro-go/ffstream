package goconv

import (
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func CustomOptionsFromGRPC(
	opts []*ffstream_grpc.CustomOption,
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
) []*ffstream_grpc.CustomOption {
	result := make([]*ffstream_grpc.CustomOption, 0, len(opts))

	for _, opt := range opts {
		result = append(result, &ffstream_grpc.CustomOption{
			Key:   opt.Key,
			Value: opt.Value,
		})
	}

	return result
}
