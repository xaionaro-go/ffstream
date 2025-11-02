package goconv

import (
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func BitRatesToGRPC(src *streammuxtypes.BitRates) *ffstream_grpc.BitRates {
	if src == nil {
		return nil
	}

	return &ffstream_grpc.BitRates{
		InputBitRate:   BitRateInfoToGRPC(src.Input),
		EncodedBitRate: BitRateInfoToGRPC(src.Encoded),
		OutputBitRate:  BitRateInfoToGRPC(src.Output),
	}
}

func BitRateInfoToGRPC(src streammuxtypes.BitRateInfo) *ffstream_grpc.BitRateInfo {
	return &ffstream_grpc.BitRateInfo{
		Audio: uint64(src.Audio),
		Video: uint64(src.Video),
		Other: uint64(src.Other),
	}
}

func BitRatesFromGRPC(src *ffstream_grpc.BitRates) *streammuxtypes.BitRates {
	if src == nil {
		return nil
	}

	return &streammuxtypes.BitRates{
		Input:   BitRateInfoFromGRPC(src.InputBitRate),
		Encoded: BitRateInfoFromGRPC(src.EncodedBitRate),
		Output:  BitRateInfoFromGRPC(src.OutputBitRate),
	}
}

func BitRateInfoFromGRPC(src *ffstream_grpc.BitRateInfo) streammuxtypes.BitRateInfo {
	if src == nil {
		return streammuxtypes.BitRateInfo{}
	}

	return streammuxtypes.BitRateInfo{
		Audio: streammuxtypes.Ubps(src.Audio),
		Video: streammuxtypes.Ubps(src.Video),
		Other: streammuxtypes.Ubps(src.Other),
	}
}
