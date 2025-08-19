package goconv

import (
	"github.com/xaionaro-go/avpipeline/node"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func ProcessingPacketsOrFramesStatisticsToGRPC(
	s *node.FramesOrPacketsStatistics,
) *ffstream_grpc.CommonsProcessingPacketsOrFramesStatistics {
	if s == nil {
		return nil
	}
	return &ffstream_grpc.CommonsProcessingPacketsOrFramesStatistics{
		Read:   ProcessingPacketsOrFramesStatisticsSectionToGRPC(&s.Read),
		Missed: ProcessingPacketsOrFramesStatisticsSectionToGRPC(&s.Missed),
		Wrote:  ProcessingPacketsOrFramesStatisticsSectionToGRPC(&s.Wrote),
	}
}

func ProcessingPacketsOrFramesStatisticsSectionToGRPC(
	s *node.FramesOrPacketsStatisticsSection,
) *ffstream_grpc.CommonsProcessingPacketsOrFramesStatisticsSection {
	if s == nil {
		return nil
	}
	return &ffstream_grpc.CommonsProcessingPacketsOrFramesStatisticsSection{
		Unknown: s.Unknown.Load(),
		Other:   s.Other.Load(),
		Video:   s.Video.Load(),
		Audio:   s.Audio.Load(),
	}
}
