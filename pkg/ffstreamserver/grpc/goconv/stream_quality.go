package goconv

import (
	quality "github.com/xaionaro-go/avpipeline/packetorframe/filter/quality/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func StreamQualityToGRPC(
	in quality.StreamQuality,
) *ffstream_grpc.StreamQuality {
	return &ffstream_grpc.StreamQuality{
		Continuity: in.Continuity,
		Overlap:    in.Overlap,
		FrameRate:  in.FrameRate,
		InvalidDts: uint64(in.InvalidDTS),
	}
}

func StreamQualityFromGRPC(
	in *ffstream_grpc.StreamQuality,
) quality.StreamQuality {
	if in == nil {
		return quality.StreamQuality{}
	}
	return quality.StreamQuality{
		Continuity: in.Continuity,
		Overlap:    in.Overlap,
		FrameRate:  in.FrameRate,
		InvalidDTS: uint(in.InvalidDts),
	}
}
