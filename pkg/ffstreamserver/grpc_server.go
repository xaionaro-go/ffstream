package ffstreamserver

import (
	"context"
	"sync"

	"github.com/xaionaro-go/avpipeline/kernel"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstream/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/goconv"
	"github.com/xaionaro-go/secret"
	"github.com/xaionaro-go/xcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GRPCServer struct {
	ffstream_grpc.UnimplementedFFStreamServer
	FFStream         *ffstream.FFStream
	locker           sync.Mutex
	stopRecodingFunc context.CancelFunc
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

func convertCustomOptionsToAVPipeline(customOptions types.DictionaryItems) avptypes.DictionaryItems {
	result := make(avptypes.DictionaryItems, 0, len(customOptions))
	for _, opt := range customOptions {
		result = append(result, avptypes.DictionaryItem{
			Key:   opt.Key,
			Value: opt.Value,
		})
	}
	return result
}

func (srv *GRPCServer) AddInput(
	ctx context.Context,
	req *ffstream_grpc.AddInputRequest,
) (*ffstream_grpc.AddInputReply, error) {
	input, err := kernel.NewInputFromURL(
		ctx,
		req.GetUrl(), secret.New(""),
		kernel.InputConfig{
			CustomOptions: convertCustomOptionsToAVPipeline(goconv.CustomOptionsFromGRPC(req.GetCustomOptions())),
		},
	)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to open the input: %v", err)
	}

	err = srv.FFStream.AddInput(ctx, input)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to add the input: %v", err)
	}

	return &ffstream_grpc.AddInputReply{
		Id: int64(input.ID),
	}, nil
}

func (srv *GRPCServer) AddOutput(
	ctx context.Context,
	req *ffstream_grpc.AddOutputRequest,
) (*ffstream_grpc.AddOutputReply, error) {
	output, err := kernel.NewOutputFromURL(
		ctx,
		req.GetUrl(), secret.New(""),
		kernel.OutputConfig{
			CustomOptions: convertCustomOptionsToAVPipeline(goconv.CustomOptionsFromGRPC(req.GetCustomOptions())),
		},
	)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to open the output: %v", err)
	}

	err = srv.FFStream.AddOutput(ctx, output)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to add the output: %v", err)
	}

	return &ffstream_grpc.AddOutputReply{
		Id: uint64(output.ID),
	}, nil
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
	err := srv.FFStream.SetRecoderConfig(ctx, goconv.RecoderConfigFromGRPC(req.GetConfig()))
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to configure the encoder: %v", err)
	}
	return &ffstream_grpc.SetRecoderConfigReply{}, nil
}

func (srv *GRPCServer) Start(
	ctx context.Context,
	req *ffstream_grpc.StartRequest,
) (*ffstream_grpc.StartReply, error) {
	srv.locker.Lock()
	defer srv.locker.Unlock()
	if srv.stopRecodingFunc != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "recoding is already started")
	}
	ctx, cancelFn := context.WithCancel(xcontext.DetachDone(ctx))
	err := srv.FFStream.Start(ctx)
	if err != nil {
		cancelFn()
		return nil, status.Errorf(codes.Unknown, "unable to start the recoding: %v", err)
	}
	srv.stopRecodingFunc = cancelFn
	return &ffstream_grpc.StartReply{}, nil
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
