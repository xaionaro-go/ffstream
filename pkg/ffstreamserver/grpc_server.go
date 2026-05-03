// grpc_server.go implements the gRPC service for FFStream.

package ffstreamserver

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/facebookincubator/go-belt"
	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/preset/inputwithfallback"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avpipeline_grpc "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
	goconvavp "github.com/xaionaro-go/avpipeline/protobuf/goconv/avpipeline"
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

	// Emit InputInfo for EVERY registered resource, including those
	// whose kernel slot has not yet opened. The wingout
	// Deactivate path enumerates priority-0 entries via this RPC and
	// issues RemoveInput on each — if a resource is omitted just because
	// its kernel hasn't opened (e.g. a freshly-added android_camera
	// whose libav demuxer is still negotiating), the UI loses track of
	// it and Deactivate silently leaks the registration.
	//
	// Lock-order contract (preserved from the previous in-locker walk
	// and from SnapshotInputsInfo godoc): take FFStream.locker BEFORE
	// InputChainsLocker. AddInput holds FFStream.locker while calling
	// AddFactory which in turn acquires InputChainsLocker; reversing
	// that order here would deadlock.
	//
	// KernelLocker is acquired via ManualTryLock (non-blocking) inside
	// the InputChainsLocker.Do closure below — this reverses the
	// canonical KernelLocker→InputChainsLocker order observed elsewhere
	// in ffstream.go, but is safe because ManualTryLock cannot deadlock:
	// a failed try-lock skips kernel correlation for that priority and
	// surfaces every resource at it with IsActive=false (the
	// "registered but not currently producing frames" reading, which
	// matches the user-visible truth when the pipeline goroutine holds
	// KernelLocker mid-Open).
	//
	// TOCTOU race window: SnapshotInputsInfo
	// (under FFStream.locker) and InputChainsLocker.Do are SEQUENTIAL,
	// not nested-held. A concurrent RemoveInput between the two
	// acquisitions can shift later entries' nums down and cause the Num
	// field in the reply to be stale relative to the (post-RemoveInput)
	// live registry. The wingout Deactivate path
	// (CamerasBuiltin.qml._removeBuiltinInputsAtPriority0) tolerates
	// this: each RemoveInput failure (NotFound on a stale num) is
	// counted into failureCount, the walk continues forward, and the
	// caller surfaces the partial-cleanup warning via
	// deactivateErrorDialog. A nested-held alternative (snapshot under
	// both locks) would deadlock against AddInput's
	// FFStream.locker→InputChainsLocker order.
	//
	// Step 1: snapshot the full InputsInfo registration under
	// FFStream.locker. The returned []Resources is a deep copy and may
	// be iterated without further synchronisation.
	snapshot := srv.FFStream.SnapshotInputsInfo(ctx)

	// Step 2: under InputChainsLocker, correlate each snapshotted
	// resource with its kernel state. A resource whose kernel has not
	// yet opened still appears in the reply with IsActive=false; the
	// previous behaviour (skip the row entirely) is now restricted to
	// the truly-impossible case where InputChains has no entry at the
	// snapshot's priority — that would indicate a bug, not a transient
	// kernel-open delay.
	var result []*ffstream_grpc.InputInfo
	srv.FFStream.Inputs.InputChainsLocker.Do(ctx, func() {
		// The InputSwitch routes packets from exactly one chain
		// downstream at a time. Its CurrentValue holds that chain's ID.
		// We treat a chain as "active" only when its kernel is open AND
		// the switch is currently selecting it — matching the user-visible
		// notion of "the input that is producing frames right now".
		currentChainID := srv.FFStream.Inputs.InputSwitch.CurrentValue.Load()
		// Build a priority -> InputChain index for O(1) correlation
		// against the snapshot. The chain count and the snapshot's
		// outer length are kept equal by AddInput's invariant
		// (boundary_test verifies this); a mismatch here is a soft
		// error logged and skipped, not a panic.
		chainsByPriority := make(map[uint]*ffstream.InputChain, len(srv.FFStream.Inputs.InputChains))
		for _, inputChain := range srv.FFStream.Inputs.InputChains {
			if inputChain == nil {
				continue
			}
			inputFactory, ok := inputChain.InputFactory.(*ffstream.InputFactory)
			if !ok {
				continue
			}
			chainsByPriority[uint(inputFactory.FallbackPriority)] = inputChain
		}

		for priority, resources := range snapshot {
			inputChain, ok := chainsByPriority[uint(priority)]
			if !ok || inputChain == nil {
				logger.Debugf(ctx, "GetInputsInfo: no InputChain for priority %d (snapshot has %d resources at this priority); skipping", priority, len(resources))
				continue
			}
			k := inputChain.Input.Processor.Kernel
			isCurrent := int32(inputChain.ID) == currentChainID

			// Resolve kernel state once per priority (the kernel
			// covers all resources at that priority via Kernel0[idx]).
			// Reading KernelIsSet and Kernel together under the same
			// KernelLocker hold matches the pre-fix race-fix invariant.
			var kernelIsSet bool
			var kernelTeeLen int
			var kernelByIdx func(int) kernel.Abstract
			func() {
				if !k.KernelLocker.ManualTryLock(ctx) {
					// KernelLocker held by the pipeline goroutine
					// (e.g. mid-Open). Treat as "kernel state
					// unobservable" — every resource at this
					// priority surfaces with IsActive=false and a
					// zero ID, which is correct: the user-visible
					// truth is "registered but not currently
					// producing frames".
					return
				}
				defer k.KernelLocker.ManualUnlock(ctx)
				kernelIsSet = k.KernelIsSet
				if k.Kernel == nil {
					return
				}
				kernelTeeLen = len(k.Kernel.Kernel0)
				// Capture the slice via a closure so callers can
				// look up specific idx values without holding the
				// lock — the inner Tee is append-only during
				// kernel-open and our snapshot of len limits the
				// access range. Note: Kernel0 itself is a value-typed
				// slice header captured here, so subsequent Tee
				// mutations by the pipeline goroutine cannot extend
				// our view; ID lookups remain safe.
				teeSnapshot := k.Kernel.Kernel0
				kernelByIdx = func(idx int) kernel.Abstract {
					if idx >= len(teeSnapshot) {
						return nil
					}
					return teeSnapshot[idx]
				}
			}()

			for idx, res := range resources {
				var (
					id       uint64
					isActive bool
				)
				if kernelByIdx != nil && idx < kernelTeeLen {
					if abs := kernelByIdx(idx); abs != nil {
						id = uint64(abs.GetObjectID())
						isActive = kernelIsSet && isCurrent
					}
				}
				result = append(result, &ffstream_grpc.InputInfo{
					Id:          id,
					Priority:    uint64(priority),
					Num:         uint64(idx),
					Url:         res.URL,
					InputConfig: goconvavp.InputConfigToProto(res.InputConfig),
					IsActive:    isActive,
					Suppressed:  res.Suppressed,
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
		if int(req.GetInputPriority()) >= len(srv.FFStream.Inputs.InputChains) {
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

	// Read and mutate the Resource's CustomOptions under FFStream.Locker,
	// because InputsInfo is mutated concurrently by AddInput and
	// SetSuppressed, and the inputFactory.GetResources() return value
	// aliases FFStream.InputsInfo[priority].
	if err := srv.FFStream.SetInputCustomOption(
		ctx,
		inputFactory.FallbackPriority,
		ffstream.ResourceIndex(req.GetInputNum()),
		req.GetKey(),
		req.GetValue(),
	); err != nil {
		return nil, status.Errorf(codes.Internal, "unable to set input custom option: %v", err)
	}

	return &ffstream_grpc.SetInputCustomOptionReply{}, nil
}

func (srv *GRPCServer) SetStopInput(
	ctx context.Context,
	req *ffstream_grpc.SetStopInputRequest,
) (*ffstream_grpc.SetStopInputReply, error) {
	ctx = srv.ctx(ctx)
	logger.Debugf(ctx, "SetStopInput: %s", spew.Sdump(req))
	defer func() { logger.Debugf(ctx, "/SetStopInput: %s", spew.Sdump(req)) }()
	// Boundary check up front so callers get InvalidArgument (not
	// Internal) for out-of-range priorities.
	if err := xsync.DoR1(ctx, &srv.FFStream.Inputs.InputChainsLocker, func() error {
		if int(req.GetInputPriority()) >= len(srv.FFStream.Inputs.InputChains) {
			return status.Errorf(codes.InvalidArgument, "input priority %d is out of range (input chains=%d)", req.GetInputPriority(), len(srv.FFStream.Inputs.InputChains))
		}
		return nil
	}); err != nil {
		return nil, err
	}

	id := inputwithfallback.InputID(req.GetInputPriority())
	switch req.GetStop() {
	case true:
		if err := srv.FFStream.Inputs.PauseChain(ctx, id); err != nil {
			return nil, status.Errorf(codes.Internal, "unable to stop input at priority %d: %v", req.GetInputPriority(), err)
		}
	case false:
		if err := srv.FFStream.Inputs.UnpauseChain(ctx, id); err != nil {
			return nil, status.Errorf(codes.Internal, "unable to resume input at priority %d: %v", req.GetInputPriority(), err)
		}
	}

	return &ffstream_grpc.SetStopInputReply{}, nil
}

func (srv *GRPCServer) SetInputSuppressed(
	ctx context.Context,
	req *ffstream_grpc.SetInputSuppressedRequest,
) (*ffstream_grpc.SetInputSuppressedReply, error) {
	ctx = srv.ctx(ctx)
	logger.Debugf(ctx, "SetInputSuppressed: %s", spew.Sdump(req))
	defer func() { logger.Debugf(ctx, "/SetInputSuppressed: %s", spew.Sdump(req)) }()

	if err := srv.FFStream.SetSuppressed(
		ctx,
		uint(req.GetInputPriority()),
		ffstream.ResourceIndex(req.GetInputNum()),
		req.GetSuppressed(),
	); err != nil {
		return nil, status.Errorf(codes.Internal, "unable to set input suppressed: %v", err)
	}

	return &ffstream_grpc.SetInputSuppressedReply{}, nil
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

func (srv *GRPCServer) SetOutputURL(
	ctx context.Context,
	req *ffstream_grpc.SetOutputURLRequest,
) (*ffstream_grpc.SetOutputURLReply, error) {
	ctx = srv.ctx(ctx)
	logger.Infof(ctx, "SetOutputURL: %q", req.GetUrl())
	if err := srv.FFStream.SetOutputURL(ctx, req.GetUrl()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "unable to set output URL: %v", err)
	}
	return &ffstream_grpc.SetOutputURLReply{}, nil
}

func (srv *GRPCServer) InjectSubtitles(
	ctx context.Context,
	req *ffstream_grpc.InjectSubtitlesRequest,
) (*ffstream_grpc.InjectSubtitlesReply, error) {
	ctx = srv.ctx(ctx)
	logger.Debugf(ctx, "InjectSubtitles: %v", req)
	if err := srv.FFStream.InjectSubtitles(ctx, req.GetData(), time.Duration(req.GetDurationNs())); err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to inject subtitles: %v", err)
	}
	return &ffstream_grpc.InjectSubtitlesReply{}, nil
}

func (srv *GRPCServer) InjectData(
	ctx context.Context,
	req *ffstream_grpc.InjectDataRequest,
) (*ffstream_grpc.InjectDataReply, error) {
	ctx = srv.ctx(ctx)
	logger.Debugf(ctx, "InjectData: %v", req)
	if err := srv.FFStream.InjectData(ctx, req.GetData(), time.Duration(req.GetDurationNs())); err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to inject data: %v", err)
	}
	return &ffstream_grpc.InjectDataReply{}, nil
}

func (srv *GRPCServer) AddInput(
	ctx context.Context,
	req *ffstream_grpc.AddInputRequest,
) (_ret *ffstream_grpc.AddInputReply, _err error) {
	ctx = srv.ctx(ctx)
	logger.Debugf(ctx, "AddInput: %s", spew.Sdump(req))
	defer func() { logger.Debugf(ctx, "/AddInput: %s: %v %v", spew.Sdump(req), _ret, _err) }()
	resource := ffstream.Resource{
		URL:         req.GetUrl(),
		Priority:    uint(req.GetPriority()),
		InputConfig: goconvavp.InputConfigFromProto(req.GetInputConfig()),
	}
	num, err := srv.FFStream.AddInput(ctx, resource)
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to add input at priority %d: %v", req.GetPriority(), err)
	}
	return &ffstream_grpc.AddInputReply{Num: uint64(num)}, nil
}

func (srv *GRPCServer) ReinitEncoder(
	ctx context.Context,
	req *ffstream_grpc.ReinitEncoderRequest,
) (_ret *ffstream_grpc.ReinitEncoderReply, _err error) {
	ctx = srv.ctx(ctx)
	logger.Debugf(ctx, "ReinitEncoder")
	defer func() { logger.Debugf(ctx, "/ReinitEncoder: %v %v", _ret, _err) }()

	dur, err := srv.FFStream.ReinitEncoder(ctx)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "unable to reinit encoder: %v", err)
	}
	return &ffstream_grpc.ReinitEncoderReply{
		DurationUs: uint64(dur.Microseconds()),
	}, nil
}

func (srv *GRPCServer) RemoveInput(
	ctx context.Context,
	req *ffstream_grpc.RemoveInputRequest,
) (_ret *ffstream_grpc.RemoveInputReply, _err error) {
	ctx = srv.ctx(ctx)
	logger.Debugf(ctx, "RemoveInput: %s", spew.Sdump(req))
	defer func() { logger.Debugf(ctx, "/RemoveInput: %s: %v %v", spew.Sdump(req), _ret, _err) }()
	if err := srv.FFStream.RemoveInput(ctx, uint(req.GetPriority()), uint(req.GetNum())); err != nil {
		if errors.Is(err, ffstream.ErrInputNotFound) {
			return nil, status.Errorf(codes.NotFound, "no input at (priority=%d, num=%d): %v", req.GetPriority(), req.GetNum(), err)
		}
		return nil, status.Errorf(codes.Unknown, "unable to remove input at (priority=%d, num=%d): %v", req.GetPriority(), req.GetNum(), err)
	}
	return &ffstream_grpc.RemoveInputReply{}, nil
}
