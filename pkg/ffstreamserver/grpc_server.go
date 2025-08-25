package ffstreamserver

import (
	"context"
	"sync"

	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avpipeline_grpc "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
	avpgoconv "github.com/xaionaro-go/avpipeline/protobuf/goconv"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/goconv"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GRPCServer struct {
	ffstream_grpc.UnimplementedFFStreamServer
	FFStream             *ffstream.FFStream
	locker               sync.Mutex
	stopRecodingFunc     context.CancelFunc
	initialRecoderConfig streammuxtypes.RecoderConfig
}

func NewGRPCServer(ffStream *ffstream.FFStream) *GRPCServer {
	return &GRPCServer{
		FFStream: ffStream,
	}
}

func (srv *GRPCServer) SetLoggingLevel(
	ctx context.Context,
	req *ffstream_grpc.SetLoggingLevelRequest,
) (*ffstream_grpc.SetLoggingLevelReply, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SetLoggingLevel not implemented, yet")
}

func (srv *GRPCServer) GetRecoderConfig(
	ctx context.Context,
	req *ffstream_grpc.GetRecoderConfigRequest,
) (*ffstream_grpc.GetRecoderConfigReply, error) {
	cfg := srv.FFStream.GetRecoderConfig(ctx)
	return &ffstream_grpc.GetRecoderConfigReply{
		Config: goconv.RecoderConfigToGRPC(cfg),
	}, nil
}

func (srv *GRPCServer) SetRecoderConfig(
	ctx context.Context,
	req *ffstream_grpc.SetRecoderConfigRequest,
) (*ffstream_grpc.SetRecoderConfigReply, error) {
	cfg := goconv.RecoderConfigFromGRPC(req.GetConfig())
	if srv.FFStream.StreamMux == nil {
		srv.initialRecoderConfig = cfg
		return &ffstream_grpc.SetRecoderConfigReply{}, nil
	}
	err := srv.FFStream.SetRecoderConfig(ctx, cfg)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to configure the encoder: %v", err)
	}
	return &ffstream_grpc.SetRecoderConfigReply{}, nil
}

func (srv *GRPCServer) GetStats(
	ctx context.Context,
	req *ffstream_grpc.GetStatsRequest,
) (*ffstream_grpc.GetStatsReply, error) {
	stats := srv.FFStream.GetStats(ctx)
	if stats == nil {
		return nil, status.Errorf(codes.Unknown, "unable to get the statistics")
	}

	return stats, nil
}

func (srv *GRPCServer) WaitChan(
	req *ffstream_grpc.WaitRequest,
	reqSrv ffstream_grpc.FFStream_WaitChanServer,
) error {
	ctx := reqSrv.Context()
	ctx, cancelFn := context.WithCancel(ctx)
	defer cancelFn()
	err := srv.FFStream.Wait(ctx)
	if err != nil {
		return status.Errorf(codes.Unknown, "unable to wait for the end: %v", err)
	}
	return reqSrv.Send(&ffstream_grpc.WaitReply{})
}

func (srv *GRPCServer) End(
	ctx context.Context,
	req *ffstream_grpc.EndRequest,
) (*ffstream_grpc.EndReply, error) {
	srv.locker.Lock()
	defer srv.locker.Unlock()
	if srv.stopRecodingFunc == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "recoding is not started")
	}
	srv.stopRecodingFunc()
	srv.stopRecodingFunc = nil
	return &ffstream_grpc.EndReply{}, nil
}

func (srv *GRPCServer) GetTolerableOutputQueueSizeBytes(
	ctx context.Context,
	req *ffstream_grpc.GetTolerableOutputQueueSizeBytesRequest,
) (*ffstream_grpc.GetTolerableOutputQueueSizeBytesReply, error) {
	return &ffstream_grpc.GetTolerableOutputQueueSizeBytesReply{
		Value: srv.FFStream.GetTolerableOutputQueueSizeBytes(ctx),
	}, nil
}

func (srv *GRPCServer) SetTolerableOutputQueueSizeBytes(
	ctx context.Context,
	req *ffstream_grpc.SetTolerableOutputQueueSizeBytesRequest,
) (*ffstream_grpc.SetTolerableOutputQueueSizeBytesReply, error) {
	srv.FFStream.SetTolerableOutputQueueSizeBytes(ctx, req.GetValue())
	return &ffstream_grpc.SetTolerableOutputQueueSizeBytesReply{}, nil
}

func (srv *GRPCServer) GetPipelines(
	ctx context.Context,
	req *ffstream_grpc.GetPipelinesRequest,
) (*ffstream_grpc.GetPipelinesResponse, error) {
	nodeInput := avpgoconv.NodeToGRPC(srv.FFStream.NodeInput)
	return &ffstream_grpc.GetPipelinesResponse{
		Nodes: []*avpipeline_grpc.Node{nodeInput},
	}, nil
}

func (srv *GRPCServer) GetAutoBitRateCalculator(
	ctx context.Context,
	req *ffstream_grpc.GetAutoBitRateCalculatorRequest,
) (*ffstream_grpc.GetAutoBitRateCalculatorReply, error) {
	calc := srv.FFStream.GetAutoBitRateCalculator(ctx)
	if calc == nil {
		return nil, status.Errorf(codes.Unknown, "unable to get the auto bitrate calculator")
	}
	calcGRPC, err := goconv.AutoBitRateCalculatorToGRPC(calc)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to convert the auto bitrate calculator to gRPC: %v", err)
	}
	return &ffstream_grpc.GetAutoBitRateCalculatorReply{
		Calculator: calcGRPC,
	}, nil
}

func (srv *GRPCServer) SetAutoBitRateCalculator(
	ctx context.Context,
	req *ffstream_grpc.SetAutoBitRateCalculatorRequest,
) (*ffstream_grpc.SetAutoBitRateCalculatorReply, error) {
	calc, err := goconv.AutoBitRateCalculatorFromGRPC(req.GetCalculator())
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to convert the auto bitrate calculator from gRPC: %v", err)
	}
	err = srv.FFStream.SetAutoBitRateCalculator(ctx, calc)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to configure the auto bitrate calculator: %v", err)
	}
	return &ffstream_grpc.SetAutoBitRateCalculatorReply{}, nil
}

func (srv *GRPCServer) GetFPSFraction(
	ctx context.Context,
	req *ffstream_grpc.GetFPSFractionRequest,
) (*ffstream_grpc.GetFPSFractionReply, error) {
	num, den, err := srv.FFStream.GetFPSFraction(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to get FPS divider: %v", err)
	}
	return &ffstream_grpc.GetFPSFractionReply{
		Num: num,
		Den: den,
	}, nil
}

func (srv *GRPCServer) SetFPSFraction(
	ctx context.Context,
	req *ffstream_grpc.SetFPSFractionRequest,
) (*ffstream_grpc.SetFPSFractionReply, error) {
	err := srv.FFStream.SetFPSFraction(ctx, req.GetNum(), req.GetDen())
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to set FPS divider: %v", err)
	}
	return &ffstream_grpc.SetFPSFractionReply{}, nil
}
