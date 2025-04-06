package ffstream

import (
	"context"
	"fmt"

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
		return typing.Opt(input.Frame.StreamIndex), nil
	case s.FFStream.Recoder.Encoder:
		inputStreamIndex := input.GetStreamIndex()
		if v, ok := s.PreviousResultsMap[inputStreamIndex]; ok {
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
			if _, ok := s.AlreadyAssignedMap[result]; !ok {
				s.PreviousResultsMap[result] = result
				return typing.Opt(result), nil
			}
			result++
		}
	default:
		return typing.Optional[int]{}, fmt.Errorf("unexpected source: %T", input.Packet.Source)
	}
}
