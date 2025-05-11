package goconv

import (
	"github.com/xaionaro-go/avpipeline/node/transcoder/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func CustomOptionsFromGRPC(
	opts []*ffstream_grpc.CustomOption,
) []types.DictionaryItem {
	result := make([]types.DictionaryItem, 0, len(opts))

	for _, opt := range opts {
		result = append(result, types.DictionaryItem{
			Key:   opt.GetKey(),
			Value: opt.GetValue(),
		})
	}

	return result
}

func CustomOptionsToGRPC(
	opts []types.DictionaryItem,
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
