package ffstreamserver

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/facebookincubator/go-belt"
	"github.com/facebookincubator/go-belt/tool/logger"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avpipeline_grpc "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
	goconvavp "github.com/xaionaro-go/avpipeline/protobuf/goconv/avpipeline"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/goconv"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GRPCServer struct {
	ffstream_grpc.UnimplementedFFStreamServer
	FFStream             *ffstream.FFStream
	Observability        *belt.Belt
	locker               sync.Mutex
	stopRecodingFunc     context.CancelFunc
	initialRecoderConfig streammuxtypes.RecoderConfig
}

func NewGRPCServer(
	ctx context.Context,
	ffStream *ffstream.FFStream,
) *GRPCServer {
	return &GRPCServer{
		Observability: belt.CtxBelt(ctx),
		FFStream:      ffStream,
	}
}

func (srv *GRPCServer) ctx(ctx context.Context) context.Context {
	return belt.CtxWithBelt(ctx, srv.Observability)
}

func (srv *GRPCServer) SetLoggingLevel(
	ctx context.Context,
	req *ffstream_grpc.SetLoggingLevelRequest,
) (*ffstream_grpc.SetLoggingLevelReply, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SetLoggingLevel not implemented, yet")
}

func (srv *GRPCServer) GetCurrentOutput(
	ctx context.Context,
	req *ffstream_grpc.GetCurrentOutputRequest,
) (*ffstream_grpc.GetCurrentOutputReply, error) {
	ctx = srv.ctx(ctx)
	cfg := srv.FFStream.GetRecoderConfig(ctx)
	return &ffstream_grpc.GetCurrentOutputReply{
		Config: goconv.RecoderConfigToGRPC(cfg),
	}, nil
}

func (srv *GRPCServer) GetStats(
	ctx context.Context,
	req *ffstream_grpc.GetStatsRequest,
) (*ffstream_grpc.GetStatsReply, error) {
	ctx = srv.ctx(ctx)
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
	ctx := srv.ctx(reqSrv.Context())
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
	ctx = srv.ctx(ctx)
	srv.locker.Lock()
	defer srv.locker.Unlock()
	if srv.stopRecodingFunc == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "recoding is not started")
	}
	srv.stopRecodingFunc()
	srv.stopRecodingFunc = nil
	return &ffstream_grpc.EndReply{}, nil
}

func (srv *GRPCServer) GetPipelines(
	ctx context.Context,
	req *ffstream_grpc.GetPipelinesRequest,
) (*ffstream_grpc.GetPipelinesResponse, error) {
	ctx = srv.ctx(ctx)
	nodeInput := goconvavp.NodeToGRPC(ctx, srv.FFStream.NodeInput)
	return &ffstream_grpc.GetPipelinesResponse{
		Nodes: []*avpipeline_grpc.Node{nodeInput},
	}, nil
}

func (srv *GRPCServer) GetAutoBitRateCalculator(
	ctx context.Context,
	req *ffstream_grpc.GetAutoBitRateCalculatorRequest,
) (_ret *ffstream_grpc.GetAutoBitRateCalculatorReply, _err error) {
	ctx = srv.ctx(ctx)
	logger.Tracef(ctx, "GetAutoBitRateCalculator(ctx, %#+v)", req)
	defer func() { logger.Tracef(ctx, "/GetAutoBitRateCalculator(ctx, %#+v): %v %v", req, _ret, _err) }()
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
) (_ret *ffstream_grpc.SetAutoBitRateCalculatorReply, _err error) {
	ctx = srv.ctx(ctx)
	logger.Tracef(ctx, "SetAutoBitRateCalculator(ctx, %#+v): %s", req.GetCalculator().GetAutoBitrateCalculator(), try(json.Marshal(req.GetCalculator().GetAutoBitrateCalculator())))
	defer func() {
		logger.Tracef(ctx, "/SetAutoBitRateCalculator(ctx, %#+v): %v %v", req.GetCalculator().GetAutoBitrateCalculator(), _ret, _err)
	}()
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
	ctx = srv.ctx(ctx)
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
	ctx = srv.ctx(ctx)
	err := srv.FFStream.SetFPSFraction(ctx, req.GetNum(), req.GetDen())
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to set FPS divider: %v", err)
	}
	return &ffstream_grpc.SetFPSFractionReply{}, nil
}

func (srv *GRPCServer) GetBitRates(
	ctx context.Context,
	req *ffstream_grpc.GetBitRatesRequest,
) (*ffstream_grpc.GetBitRatesReply, error) {
	ctx = srv.ctx(ctx)
	bitRates, err := srv.FFStream.GetBitRates(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to get bit rates: %v", err)
	}

	return &ffstream_grpc.GetBitRatesReply{
		BitRates: goconv.BitRatesToGRPC(bitRates),
	}, nil
}

func (srv *GRPCServer) GetLatencies(
	ctx context.Context,
	req *ffstream_grpc.GetLatenciesRequest,
) (*ffstream_grpc.GetLatenciesReply, error) {
	ctx = srv.ctx(ctx)
	latencies, err := srv.FFStream.GetLatencies(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to get latencies: %v", err)
	}

	return &ffstream_grpc.GetLatenciesReply{
		Latencies: goconv.LatenciesToGRPC(latencies),
	}, nil
}
