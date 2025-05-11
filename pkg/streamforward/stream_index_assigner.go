package streamforward

import (
	"context"
	"fmt"

	"github.com/asticode/go-astiav"
	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/processor"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/typing"
	"github.com/xaionaro-go/xsync"
)

type streamIndexAssigner[C any, P processor.Abstract] struct {
	StreamForward      *StreamForward[C, P]
	PreviousResultsMap map[int]int
	AlreadyAssignedMap map[int]struct{}
	Locker             xsync.Mutex
}

func newStreamIndexAssigner[C any, P processor.Abstract](s *StreamForward[C, P]) *streamIndexAssigner[C, P] {
	return &streamIndexAssigner[C, P]{
		StreamForward:      s,
		PreviousResultsMap: make(map[int]int),
		AlreadyAssignedMap: make(map[int]struct{}),
	}
}

func (s *streamIndexAssigner[C, P]) StreamIndexAssign(
	ctx context.Context,
	input avptypes.InputPacketOrFrameUnion,
) (typing.Optional[int], error) {
	return xsync.DoA2R2(ctx, &s.Locker, s.streamIndexAssign, ctx, input)
}

func (s *streamIndexAssigner[C, P]) streamIndexAssign(
	ctx context.Context,
	input avptypes.InputPacketOrFrameUnion,
) (typing.Optional[int], error) {
	switch input.Packet.Source {
	case s.StreamForward.inputAsPacketSource:
		logger.Tracef(ctx, "passing through index %d as is", input.GetStreamIndex())
		return typing.Opt(input.GetStreamIndex()), nil
	case s.StreamForward.Recoder, s.StreamForward.Recoder.Encoder:
		inputStreamIndex := input.GetStreamIndex()
		if v, ok := s.PreviousResultsMap[inputStreamIndex]; ok {
			logger.Debugf(ctx, "reassigning %d as %d (cache)", inputStreamIndex, v)
			return typing.Opt(v), nil
		}

		maxStreamIndex := 0
		s.StreamForward.inputAsPacketSource.WithFormatContext(ctx, func(fmtCtx *astiav.FormatContext) {
			for _, stream := range fmtCtx.Streams() {
				if stream.Index() > maxStreamIndex {
					maxStreamIndex = stream.Index()
				}
			}
		})

		result := maxStreamIndex + 1
		for {
			if _, ok := s.AlreadyAssignedMap[result]; ok {
				result++
				continue
			}
			s.PreviousResultsMap[inputStreamIndex] = result
			s.AlreadyAssignedMap[result] = struct{}{}
			logger.Debugf(ctx, "reassigning %d as %d", inputStreamIndex, result)
			return typing.Opt(result), nil
		}
	default:
		return typing.Optional[int]{}, fmt.Errorf("unexpected source: %T", input.Packet.Source)
	}
}
