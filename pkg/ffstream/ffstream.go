package ffstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/node"
	transcoder "github.com/xaionaro-go/avpipeline/preset/transcoderwithpassthrough"
	transcodertypes "github.com/xaionaro-go/avpipeline/preset/transcoderwithpassthrough/types"
	"github.com/xaionaro-go/avpipeline/processor"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/observability"
)

type FFStream struct {
	NodeInput   *node.Node[*processor.FromKernel[*kernel.Input]]
	NodeOutputs []*node.Node[*processor.FromKernel[*kernel.Output]]

	StreamForward *transcoder.TranscoderWithPassthrough[struct{}, *processor.FromKernel[*kernel.Input]]

	cancelFunc context.CancelFunc
	locker     sync.Mutex
}

func New(ctx context.Context) *FFStream {
	s := &FFStream{}
	return s
}

func (s *FFStream) addCancelFnLocked(cancelFn context.CancelFunc) {
	if s.cancelFunc == nil {
		s.cancelFunc = cancelFn
		return
	}

	oldCancelFn := s.cancelFunc
	s.cancelFunc = func() {
		cancelFn()
		oldCancelFn()
	}
}

func (s *FFStream) AddInput(
	ctx context.Context,
	input *kernel.Input,
) error {
	s.locker.Lock()
	defer s.locker.Unlock()
	if s.NodeInput != nil {
		return fmt.Errorf("currently we support only one input")
	}
	ctx, cancelFn := context.WithCancel(ctx)
	s.addCancelFnLocked(cancelFn)
	s.NodeInput = node.NewFromKernel(ctx, input, processor.DefaultOptionsInput()...)
	return nil
}

func (s *FFStream) AddOutput(
	ctx context.Context,
	output *kernel.Output,
) error {
	s.locker.Lock()
	defer s.locker.Unlock()
	ctx, cancelFn := context.WithCancel(ctx)
	s.addCancelFnLocked(cancelFn)
	s.NodeOutputs = append(s.NodeOutputs, node.NewFromKernel(ctx, output, processor.DefaultOptionsOutput()...))
	return nil
}

func (s *FFStream) GetRecoderConfig(
	ctx context.Context,
) (_ret transcodertypes.RecoderConfig) {
	return s.StreamForward.GetRecoderConfig(ctx)
}

func (s *FFStream) SetRecoderConfig(
	ctx context.Context,
	cfg transcodertypes.RecoderConfig,
) (_err error) {
	logger.Debugf(ctx, "SetRecoderConfig(ctx, %#+v)", cfg)
	defer func() { logger.Debugf(ctx, "/SetRecoderConfig(ctx, %#+v): %v", cfg, _err) }()
	if s.StreamForward == nil {
		return fmt.Errorf("it is allowed to use SetRecoderConfig only after Start is invoked")
	}
	return s.StreamForward.SetRecoderConfig(ctx, cfg)
}

func (s *FFStream) GetStats(
	ctx context.Context,
) *ffstream_grpc.GetStatsReply {
	// TODO: fix me: add support of multiple outputs
	return &ffstream_grpc.GetStatsReply{
		BytesCountRead:  s.NodeInput.Statistics.BytesCountWrote.Load(),
		BytesCountWrote: s.NodeOutputs[0].Statistics.BytesCountRead.Load(),
		FramesRead: &ffstream_grpc.CommonsProcessingFramesStatistics{
			Unknown: s.NodeInput.Statistics.FramesWrote.Unknown.Load(),
			Other:   s.NodeInput.Statistics.FramesWrote.Other.Load(),
			Video:   s.NodeInput.Statistics.FramesWrote.Video.Load(),
			Audio:   s.NodeInput.Statistics.FramesWrote.Audio.Load(),
		},
		FramesMissed: &ffstream_grpc.CommonsProcessingFramesStatistics{
			Unknown: s.StreamForward.NodeRecoder.Statistics.FramesMissed.Unknown.Load(),
			Other:   s.StreamForward.NodeRecoder.Statistics.FramesMissed.Other.Load(),
			Video:   s.StreamForward.NodeRecoder.Statistics.FramesMissed.Video.Load(),
			Audio:   s.StreamForward.NodeRecoder.Statistics.FramesMissed.Audio.Load(),
		},
		FramesWrote: &ffstream_grpc.CommonsProcessingFramesStatistics{
			Unknown: s.NodeOutputs[0].Statistics.FramesRead.Unknown.Load(),
			Other:   s.NodeOutputs[0].Statistics.FramesRead.Other.Load(),
			Video:   s.NodeOutputs[0].Statistics.FramesRead.Video.Load(),
			Audio:   s.NodeOutputs[0].Statistics.FramesRead.Audio.Load(),
		},
	}
}

func (s *FFStream) GetAllStats(
	ctx context.Context,
) map[string]*node.ProcessingStatistics {
	return s.StreamForward.GetAllStats(ctx)
}

func (s *FFStream) Start(
	ctx context.Context,
	recoderConfig transcodertypes.RecoderConfig,
	passthroughMode transcodertypes.PassthroughMode,
	passthroughEncoderByDefault bool,
) (_err error) {
	logger.Debugf(ctx, "Start")
	defer func() { logger.Debugf(ctx, "/Start: %v", _err) }()

	if s.StreamForward != nil {
		return fmt.Errorf("this ffstream was already used")
	}
	if s.NodeInput == nil {
		return fmt.Errorf("no inputs added")
	}
	if len(s.NodeOutputs) == 0 {
		return fmt.Errorf("no outputs added")
	}

	var nodeOutputs []node.Abstract
	for _, nodeOutput := range s.NodeOutputs {
		nodeOutputs = append(nodeOutputs, nodeOutput)
	}

	ctx, cancelFn := context.WithCancel(ctx)
	s.addCancelFnLocked(cancelFn)

	var err error
	s.StreamForward, err = transcoder.New[struct{}, *processor.FromKernel[*kernel.Input]](
		ctx,
		s.NodeInput.Processor.Kernel,
		nodeOutputs...,
	)
	if err != nil {
		return fmt.Errorf("unable to initialize a StreamForward: %w", err)
	}
	s.NodeInput.AddPushPacketsTo(s.StreamForward.Input())

	if err := s.SetRecoderConfig(ctx, recoderConfig); err != nil {
		return fmt.Errorf("SetRecoderConfig(%#+v): %w", recoderConfig, err)
	}

	if passthroughEncoderByDefault {
		logger.Infof(ctx, "passing through the encoder due to the flag provided")
		s.StreamForward.PassthroughSwitch.CurrentValue.Store(1)
		s.StreamForward.PostSwitchFilter.CurrentValue.Store(1)
		s.StreamForward.PassthroughSwitch.NextValue.Store(1)
		s.StreamForward.PostSwitchFilter.NextValue.Store(1)
	}

	err = s.StreamForward.Start(ctx, passthroughMode)
	if err != nil {
		return fmt.Errorf("unable to start the StreamForward: %w", err)
	}

	errCh := make(chan node.Error, 100)
	observability.Go(ctx, func(ctx context.Context) {
		defer close(errCh)
		var wg sync.WaitGroup
		defer wg.Wait()
		wg.Add(1)
		observability.Go(ctx, func(ctx context.Context) {
			defer wg.Done()
			s.NodeInput.Serve(ctx, node.ServeConfig{}, errCh)
		})
		wg.Add(1)
		observability.Go(ctx, func(ctx context.Context) {
			wg.Done()
			avpipeline.Serve(ctx, avpipeline.ServeConfig{}, errCh, s.NodeOutputs...)
		})
	})

	observability.Go(ctx, func(ctx context.Context) {
		defer s.cancelFunc()
		select {
		case <-ctx.Done():
		case err, ok := <-errCh:
			if !ok {
				logger.Debugf(ctx, "the error channel is closed")
				return
			}

			if errors.Is(err.Err, context.Canceled) {
				logger.Debugf(ctx, "cancelled: %#+v", err)
				return
			}
			if errors.Is(err.Err, io.EOF) {
				logger.Debugf(ctx, "EOF: %#+v", err)
				return
			}
			logger.Errorf(ctx, "stopping because received error: %v", err)
			return
		}
	})

	return nil
}

func (s *FFStream) Wait(
	ctx context.Context,
) error {
	return s.StreamForward.Wait(ctx)
}
