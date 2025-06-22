package ffstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/node"
	packetfiltercondition "github.com/xaionaro-go/avpipeline/node/filter/packetfilter/condition"
	"github.com/xaionaro-go/avpipeline/node/filter/packetfilter/preset/videodropnonkeyframes"
	transcoder "github.com/xaionaro-go/avpipeline/preset/transcoderwithpassthrough"
	transcodertypes "github.com/xaionaro-go/avpipeline/preset/transcoderwithpassthrough/types"
	"github.com/xaionaro-go/avpipeline/processor"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/observability"
	"github.com/xaionaro-go/xsync"
)

const (
	setSmallBuffer = false
)

type FFStream struct {
	NodeInput   *node.Node[*processor.FromKernel[*kernel.Input]]
	NodeOutputs []*node.Node[*processor.FromKernel[*kernel.Output]]

	StreamForward *transcoder.TranscoderWithPassthrough[struct{}, *processor.FromKernel[*kernel.Input]]

	TolerableOutputQueueSizeBytes atomic.Uint64
	CurrentOutputBufferSize       xsync.Map[int, uint64]

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

	outputChannelSize := 600
	if setSmallBuffer {
		logger.Debugf(ctx, "setting the small output buffer")
		// setting the (kernel-side) send buffer size low enough to be manage the buffers manually in the user space (our-side)
		if err := output.SetSendBufferSize(ctx, 65536); err != nil {
			logger.Errorf(ctx, "unable to set the send buffer size: %v", err)
		}
		outputChannelSize = 10 // keeping it small to be able to drop incoming packets to reduce traffic (see the usages of `GetTolerableOutputQueueSizeBytes`).
	}

	s.NodeOutputs = append(
		s.NodeOutputs,
		node.NewFromKernel(
			ctx,
			output,
			processor.OptionQueueSizeInputPacket(outputChannelSize),
			processor.OptionQueueSizeOutputPacket(0),
			processor.OptionQueueSizeError(100),
		),
	)
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
	if s == nil {
		return nil
	}
	r := &ffstream_grpc.GetStatsReply{
		FramesDropped: &ffstream_grpc.CommonsProcessingFramesStatistics{},
		FramesWrote:   &ffstream_grpc.CommonsProcessingFramesStatistics{},
	}
	if s.NodeInput != nil {
		r.BytesCountRead = s.NodeInput.Statistics.BytesCountWrote.Load()
		r.FramesRead = &ffstream_grpc.CommonsProcessingFramesStatistics{
			Unknown: s.NodeInput.Statistics.FramesWrote.Unknown.Load(),
			Other:   s.NodeInput.Statistics.FramesWrote.Other.Load(),
			Video:   s.NodeInput.Statistics.FramesWrote.Video.Load(),
			Audio:   s.NodeInput.Statistics.FramesWrote.Audio.Load(),
		}
	}
	if s.StreamForward != nil {
		r.FramesMissed = &ffstream_grpc.CommonsProcessingFramesStatistics{
			Unknown: s.StreamForward.NodeRecoder.Statistics.FramesMissed.Unknown.Load(),
			Other:   s.StreamForward.NodeRecoder.Statistics.FramesMissed.Other.Load(),
			Video:   s.StreamForward.NodeRecoder.Statistics.FramesMissed.Video.Load(),
			Audio:   s.StreamForward.NodeRecoder.Statistics.FramesMissed.Audio.Load(),
		}
	}
	for idx, nodeOutput := range s.NodeOutputs {
		stats := nodeOutput.Statistics.Convert()
		r.BytesCountWrote += stats.BytesCountRead
		r.FramesWrote.Unknown += stats.FramesRead.Unknown
		r.FramesWrote.Other += stats.FramesRead.Other
		r.FramesWrote.Video += stats.FramesRead.Video
		r.FramesWrote.Audio += stats.FramesRead.Audio

		pf := nodeOutput.GetInputPacketFilter()
		if pf == nil {
			logger.Errorf(ctx, "packet filter is not set for output %d", idx)
			continue
		}

		softPacketDropper, ok := pf.(*videodropnonkeyframes.PacketFilter)
		if !ok {
			logger.Errorf(ctx, "the packet filter on output %d is not a videodropnonkeyframes filter", idx)
			continue
		}

		bufSize, _ := s.CurrentOutputBufferSize.Load(idx)
		r.BytesCountBuffered += bufSize
		r.BytesCountDropped += softPacketDropper.TotalDroppedBytes
		r.FramesDropped.Video += softPacketDropper.TotalDroppedPackets
	}
	return r
}

func (s *FFStream) GetAllStats(
	ctx context.Context,
) map[string]*node.ProcessingStatistics {
	return s.StreamForward.GetAllStats(ctx)
}

func (s *FFStream) SetTolerableOutputQueueSizeBytes(
	ctx context.Context,
	newValue uint64,
) {
	s.TolerableOutputQueueSizeBytes.Store(newValue)
}

func (s *FFStream) GetTolerableOutputQueueSizeBytes(
	ctx context.Context,
) uint64 {
	return s.TolerableOutputQueueSizeBytes.Load()
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
	for idx, nodeOutput := range s.NodeOutputs {
		idx := idx
		var softPacketDropper *videodropnonkeyframes.PacketFilter
		softPacketDropper = videodropnonkeyframes.New(
			packetfiltercondition.Function(func(
				ctx context.Context,
				in packetfiltercondition.Input,
			) bool {
				var preOutputNode node.Abstract
				switch idx {
				case 0:
					preOutputNode = s.StreamForward.NodeStreamFixerMain.Output()
				case 1:
					preOutputNode = s.StreamForward.NodeStreamFixerPassthrough.Output()
				default:
					panic(fmt.Errorf("too many outputs"))
				}
				preOutputStats := preOutputNode.GetStatistics()
				outputStats := nodeOutput.Statistics
				totalDroppedBytes := softPacketDropper.TotalDroppedBytes
				queueSizeBytes := 0 +
					preOutputStats.BytesCountWrote.Load() -
					outputStats.BytesCountRead.Load() -
					totalDroppedBytes
				s.CurrentOutputBufferSize.Store(idx, queueSizeBytes)
				threshold := s.GetTolerableOutputQueueSizeBytes(ctx)
				logger.Tracef(ctx, "output %d: queue size: %d (/%d); total dropped so far: %d", idx, queueSizeBytes, threshold, totalDroppedBytes)
				if threshold <= 0 {
					return true
				}
				return queueSizeBytes <= threshold
			}),
		)
		nodeOutput.SetInputPacketFilter(softPacketDropper)
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

	err = s.StreamForward.Start(ctx, passthroughMode, avpipeline.ServeConfig{
		EachNode: node.ServeConfig{
			FrameDrop: false, // we are dropping frames in a more controlled manner above (look for `SetInputPacketFilter`).
		},
	})
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
