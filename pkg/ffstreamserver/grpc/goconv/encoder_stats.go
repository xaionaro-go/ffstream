package goconv

import (
	"github.com/xaionaro-go/avpipeline"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func EncoderStatsToGRPC(
	req *avpipeline.NodeStatistics,
) *ffstream_grpc.GetEncoderStatsReply {
	return &ffstream_grpc.GetEncoderStatsReply{
		BytesCountRead:  req.BytesCountRead.Load(),
		BytesCountWrote: req.BytesCountWrote.Load(),
		FramesRead:      FramesStatisticsToGRPC(&req.FramesRead),
		FramesMissed:    FramesStatisticsToGRPC(&req.FramesMissed),
		FramesWrote:     FramesStatisticsToGRPC(&req.FramesWrote),
	}
}

func EncoderStatsFromGRPC(
	req *ffstream_grpc.GetEncoderStatsReply,
) *avpipeline.NodeStatistics {
	result := &avpipeline.NodeStatistics{
		FramesRead:   *FramesStatisticsFromGRPC(req.GetFramesRead()),
		FramesMissed: *FramesStatisticsFromGRPC(req.GetFramesMissed()),
		FramesWrote:  *FramesStatisticsFromGRPC(req.GetFramesWrote()),
	}
	result.BytesCountRead.Store(req.BytesCountRead)
	result.BytesCountWrote.Store(req.BytesCountWrote)
	return result
}
