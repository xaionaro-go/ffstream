package goconv

import (
	"github.com/xaionaro-go/avpipeline"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func FramesStatisticsToGRPC(
	stats *avpipeline.FramesStatistics,
) *ffstream_grpc.CommonsProcessingFramesStatistics {
	return &ffstream_grpc.CommonsProcessingFramesStatistics{
		Unknown: stats.Unknown.Load(),
		Other:   stats.Other.Load(),
		Video:   stats.Video.Load(),
		Audio:   stats.Audio.Load(),
	}
}

func FramesStatisticsFromGRPC(
	stats *ffstream_grpc.CommonsProcessingFramesStatistics,
) *avpipeline.FramesStatistics {
	result := &avpipeline.FramesStatistics{}
	result.Unknown.Store(stats.Unknown)
	result.Other.Store(stats.Other)
	result.Video.Store(stats.Video)
	result.Audio.Store(stats.Audio)
	return result
}
