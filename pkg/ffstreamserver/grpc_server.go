package ffstreamserver

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/davecgh/go-spew/spew"
	"github.com/facebookincubator/go-belt"
	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/kernel"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avpipeline_grpc "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
	goconvavp "github.com/xaionaro-go/avpipeline/protobuf/goconv/avpipeline"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/goconv"
	"github.com/xaionaro-go/xsync"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GRPCServer struct {
	ffstream_grpc.UnimplementedFFStreamServer
	FFStream            *ffstream.FFStream
	Observability       *belt.Belt
	locker              sync.Mutex
	stopTranscodingFunc context.CancelFunc
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
	cfg := srv.FFStream.GetTranscoderConfig(ctx)
	return &ffstream_grpc.GetCurrentOutputReply{
		Config: goconv.TranscoderConfigToGRPC(cfg),
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
	_ = ctx
	srv.locker.Lock()
	defer srv.locker.Unlock()
	if srv.stopTranscodingFunc == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "transcoding is not started")
	}
	srv.stopTranscodingFunc()
	srv.stopTranscodingFunc = nil
	return &ffstream_grpc.EndReply{}, nil
}

func (srv *GRPCServer) GetPipelines(
	ctx context.Context,
	req *ffstream_grpc.GetPipelinesRequest,
) (*ffstream_grpc.GetPipelinesResponse, error) {
	ctx = srv.ctx(ctx)
	var result []*avpipeline_grpc.Node
	for _, node := range srv.FFStream.Inputs.GetInputs(ctx).NonNil() {
		nodeInput := goconvavp.NodeToGRPC(ctx, node)
		result = append(result, nodeInput)
	}
	return &ffstream_grpc.GetPipelinesResponse{
		Nodes: result,
	}, nil
}

func (srv *GRPCServer) GetVideoAutoBitRateConfig(
	ctx context.Context,
	req *ffstream_grpc.GetVideoAutoBitRateConfigRequest,
) (_ret *ffstream_grpc.GetVideoAutoBitRateConfigReply, _err error) {
	ctx = srv.ctx(ctx)
	logger.Tracef(ctx, "GetVideoAutoBitRateConfig(ctx, %#+v)", req)
	defer func() { logger.Tracef(ctx, "/GetVideoAutoBitRateConfig(ctx, %#+v): %v %v", req, _ret, _err) }()

	cfg, err := srv.FFStream.GetAutoBitRateVideoConfig(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to get video auto bitrate config: %v", err)
	}
	cfgGRPC, err := goconvavp.AutoBitRateVideoConfigToProto(cfg)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to convert video auto bitrate config to gRPC: %v", err)
	}
	return &ffstream_grpc.GetVideoAutoBitRateConfigReply{
		Config: cfgGRPC,
	}, nil
}

func (srv *GRPCServer) SetVideoAutoBitRateConfig(
	ctx context.Context,
	req *ffstream_grpc.SetVideoAutoBitRateConfigRequest,
) (_ret *ffstream_grpc.SetVideoAutoBitRateConfigReply, _err error) {
	ctx = srv.ctx(ctx)
	logger.Tracef(ctx, "SetVideoAutoBitRateConfig(ctx, %#+v): %s", req.GetConfig(), try(json.Marshal(req.GetConfig())))
	defer func() {
		logger.Tracef(ctx, "/SetVideoAutoBitRateConfig(ctx, %#+v): %v %v", req.GetConfig(), _ret, _err)
	}()

	cfg, err := goconvavp.AutoBitRateVideoConfigFromProto(req.GetConfig())
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to convert video auto bitrate config from gRPC: %v", err)
	}
	if err := srv.FFStream.SetAutoBitRateVideoConfig(ctx, cfg); err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to configure video auto bitrate: %v", err)
	}
	return &ffstream_grpc.SetVideoAutoBitRateConfigReply{}, nil
}

func (srv *GRPCServer) GetVideoAutoBitRateCalculator(
	ctx context.Context,
	req *ffstream_grpc.GetVideoAutoBitRateCalculatorRequest,
) (_ret *ffstream_grpc.GetVideoAutoBitRateCalculatorReply, _err error) {
	ctx = srv.ctx(ctx)
	logger.Tracef(ctx, "GetVideoAutoBitRateCalculator(ctx, %#+v)", req)
	defer func() { logger.Tracef(ctx, "/GetVideoAutoBitRateCalculator(ctx, %#+v): %v %v", req, _ret, _err) }()
	calc := srv.FFStream.GetAutoBitRateCalculator(ctx)
	if calc == nil {
		return nil, status.Errorf(codes.Unknown, "unable to get the auto bitrate calculator")
	}
	calcGRPC, err := goconvavp.AutoBitRateCalculatorToProto(calc)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to convert the auto bitrate calculator to gRPC: %v", err)
	}
	return &ffstream_grpc.GetVideoAutoBitRateCalculatorReply{
		Calculator: calcGRPC,
	}, nil
}

func (srv *GRPCServer) SetVideoAutoBitRateCalculator(
	ctx context.Context,
	req *ffstream_grpc.SetVideoAutoBitRateCalculatorRequest,
) (_ret *ffstream_grpc.SetVideoAutoBitRateCalculatorReply, _err error) {
	ctx = srv.ctx(ctx)
	logger.Tracef(ctx, "SetVideoAutoBitRateCalculator(ctx, %#+v): %s", req.GetCalculator().GetAutoBitrateCalculator(), try(json.Marshal(req.GetCalculator().GetAutoBitrateCalculator())))
	defer func() {
		logger.Tracef(ctx, "/SetVideoAutoBitRateCalculator(ctx, %#+v): %v %v", req.GetCalculator().GetAutoBitrateCalculator(), _ret, _err)
	}()
	calc, err := goconvavp.AutoBitRateCalculatorFromProto(req.GetCalculator())
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to convert the auto bitrate calculator from gRPC: %v", err)
	}
	err = srv.FFStream.SetAutoBitRateCalculator(ctx, calc)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to configure the auto bitrate calculator: %v", err)
	}
	return &ffstream_grpc.SetVideoAutoBitRateCalculatorReply{}, nil
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

func (srv *GRPCServer) GetInputQuality(
	ctx context.Context,
	req *ffstream_grpc.GetInputQualityRequest,
) (*ffstream_grpc.GetInputQualityReply, error) {
	ctx = srv.ctx(ctx)
	inputQuality, err := srv.FFStream.GetInputQuality(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to get input quality measurements: %v", err)
	}
	return &ffstream_grpc.GetInputQualityReply{
		Audio: goconv.StreamQualityToGRPC(inputQuality.Audio),
		Video: goconv.StreamQualityToGRPC(inputQuality.Video),
	}, nil
}

func (srv *GRPCServer) GetOutputQuality(
	ctx context.Context,
	req *ffstream_grpc.GetOutputQualityRequest,
) (*ffstream_grpc.GetOutputQualityReply, error) {
	ctx = srv.ctx(ctx)
	outputQuality, err := srv.FFStream.GetOutputQuality(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to get output quality measurements: %v", err)
	}
	return &ffstream_grpc.GetOutputQualityReply{
		Audio: goconv.StreamQualityToGRPC(outputQuality.Audio),
		Video: goconv.StreamQualityToGRPC(outputQuality.Video),
	}, nil
}

func (srv *GRPCServer) GetInputsInfo(
	ctx context.Context,
	req *ffstream_grpc.GetInputsInfoRequest,
) (*ffstream_grpc.GetInputsInfoReply, error) {
	ctx = srv.ctx(ctx)

	var result []*ffstream_grpc.InputInfo
	srv.FFStream.Inputs.InputChainsLocker.Do(ctx, func() {
		for _, inputChain := range srv.FFStream.Inputs.InputChains {
			k := inputChain.Input.Processor.Kernel
			inputFactory := inputChain.InputFactory.(*ffstream.InputFactory)
			resources, err := inputFactory.GetResources(ctx)
			if err != nil {
				logger.Errorf(ctx, "unable to get resources for input factory: %v", err)
				continue
			}
			for idx, res := range resources {
				inputKernel := func() *kernel.Input {
					if !k.KernelLocker.ManualTryLock(ctx) {
						return nil
					}
					defer k.KernelLocker.ManualUnlock(ctx)
					if k.Kernel == nil {
						return nil
					}
					if len(k.Kernel.Kernel0) < idx {
						return nil
					}
					return k.Kernel.Kernel0[idx]
				}()
				result = append(result, &ffstream_grpc.InputInfo{
					Id:          uint64(inputKernel.GetObjectID()),
					Priority:    uint64(inputFactory.FallbackPriority),
					Num:         uint64(idx),
					Url:         res.URL,
					InputConfig: goconvavp.InputConfigToProto(res.InputConfig),
					IsActive:    k.KernelIsSet,
				})
			}
		}
	})

	return &ffstream_grpc.GetInputsInfoReply{
		Inputs: result,
	}, nil
}

func (srv *GRPCServer) SetInputCustomOption(
	ctx context.Context,
	req *ffstream_grpc.SetInputCustomOptionRequest,
) (_ret *ffstream_grpc.SetInputCustomOptionReply, _err error) {
	ctx = srv.ctx(ctx)
	logger.Debugf(ctx, "SetInputCustomOption: %s", spew.Sdump(req))
	defer func() { logger.Debugf(ctx, "/SetInputCustomOption: %s: %v %v", spew.Sdump(req), _ret, _err) }()
	inputChain, err := xsync.DoR2(ctx, &srv.FFStream.Inputs.InputChainsLocker, func() (*ffstream.InputChain, error) {
		if int(req.GetInputPriority()) > len(srv.FFStream.Inputs.InputChains) {
			return nil, status.Errorf(codes.InvalidArgument, "input priority %d is out of range (input chains=%d)", req.GetInputPriority(), len(srv.FFStream.Inputs.InputChains))
		}
		return srv.FFStream.Inputs.InputChains[req.GetInputPriority()], nil
	})
	if err != nil {
		return nil, err
	}

	inputFactory := inputChain.InputFactory.(*ffstream.InputFactory)
	if uint64(inputFactory.FallbackPriority) != req.GetInputPriority() {
		return nil, status.Errorf(codes.Internal, "input factory priority %d does not match the requested input priority %d", inputFactory.FallbackPriority, req.GetInputPriority())
	}

	resources, err := inputFactory.GetResources(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to get resources for input factory: %v", err)
	}

	if int(req.GetInputNum()) >= len(resources) {
		return nil, status.Errorf(codes.InvalidArgument, "input num %d is out of range (resources=%d)", req.GetInputNum(), len(resources))
	}
	resources[req.GetInputNum()].CustomOptions.SetFirst(avptypes.DictionaryItem{
		Key:   req.GetKey(),
		Value: req.GetValue(),
	})

	return &ffstream_grpc.SetInputCustomOptionReply{}, nil
}

func (srv *GRPCServer) SetStopInput(
	ctx context.Context,
	req *ffstream_grpc.SetStopInputRequest,
) (*ffstream_grpc.SetStopInputReply, error) {
	ctx = srv.ctx(ctx)
	logger.Debugf(ctx, "SetStopInput: %s", spew.Sdump(req))
	defer func() { logger.Debugf(ctx, "/SetStopInput: %s", spew.Sdump(req)) }()
	inputChain, err := xsync.DoR2(ctx, &srv.FFStream.Inputs.InputChainsLocker, func() (*ffstream.InputChain, error) {
		if int(req.GetInputPriority()) > len(srv.FFStream.Inputs.InputChains) {
			return nil, status.Errorf(codes.InvalidArgument, "input priority %d is out of range (input chains=%d)", req.GetInputPriority(), len(srv.FFStream.Inputs.InputChains))
		}
		return srv.FFStream.Inputs.InputChains[req.GetInputPriority()], nil
	})
	if err != nil {
		return nil, err
	}

	switch req.GetStop() {
	case true:
		err := inputChain.Pause(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "unable to stop input at priority %d: %v", req.GetInputPriority(), err)
		}
	case false:
		err := inputChain.Unpause(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "unable to resume input at priority %d: %v", req.GetInputPriority(), err)
		}
	}

	return &ffstream_grpc.SetStopInputReply{}, nil
}

func (srv *GRPCServer) SwitchOutputByProps(
	ctx context.Context,
	req *ffstream_grpc.SwitchOutputByPropsRequest,
) (*ffstream_grpc.SwitchOutputByPropsReply, error) {
	ctx = srv.ctx(ctx)
	logger.Debugf(ctx, "SwitchOutputByProps: %v", req)
	props := streammuxtypes.SenderProps{
		TranscoderConfig: goconv.TranscoderConfigFromGRPC(req.GetConfig()),
	}
	if err := srv.FFStream.SwitchOutputByProps(ctx, props); err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to switch output: %v", err)
	}
	return &ffstream_grpc.SwitchOutputByPropsReply{}, nil
}
