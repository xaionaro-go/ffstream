package ffstream

import (
	"context"
	"fmt"

	"github.com/facebookincubator/go-belt/tool/logger"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/typing"
	"github.com/xaionaro-go/xsync"
)

type streamIndexAssigner struct {
	FFStream           *FFStream
	PreviousResultsMap map[int]int
	AlreadyAssignedMap map[int]struct{}
	Locker             xsync.Mutex
}

func newStreamIndexAssigner(f *FFStream) *streamIndexAssigner {
	return &streamIndexAssigner{
		FFStream:           f,
		PreviousResultsMap: make(map[int]int),
		AlreadyAssignedMap: make(map[int]struct{}),
	}
}

func (s *streamIndexAssigner) StreamIndexAssign(
	ctx context.Context,
	input avptypes.InputPacketOrFrameUnion,
) (typing.Optional[int], error) {
	return xsync.DoA2R2(ctx, &s.Locker, s.streamIndexAssign, ctx, input)
}

func (s *streamIndexAssigner) streamIndexAssign(
	ctx context.Context,
	input avptypes.InputPacketOrFrameUnion,
) (typing.Optional[int], error) {
	switch input.Packet.Source {
	case s.FFStream.Input:
		logger.Tracef(ctx, "passing through index %d as is", input.GetStreamIndex())
		return typing.Opt(input.GetStreamIndex()), nil
	case s.FFStream.Recoder, s.FFStream.Recoder.Encoder:
		inputStreamIndex := input.GetStreamIndex()
		if v, ok := s.PreviousResultsMap[inputStreamIndex]; ok {
			logger.Debugf(ctx, "reassigning %d as %d (cache)", inputStreamIndex, v)
			return typing.Opt(v), nil
		}

		maxStreamIndex := 0
		for _, stream := range s.FFStream.Input.FormatContext.Streams() {
			if stream.Index() > maxStreamIndex {
				maxStreamIndex = stream.Index()
			}
		}

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
