//go:build none
// +build none

package ffstream

import (
	"context"

	"github.com/xaionaro-go/avpipeline/packet"
	"github.com/xaionaro-go/avpipeline/packet/condition/extra"
)

type outputStreamSourceChangeHandler struct {
	*FFStream
}

var _ extra.OnStreamSourceChangeHandler = (*outputStreamSourceChangeHandler)(nil)

func newOutputStreamSourceChangeHandler(
	s *FFStream,
) *outputStreamSourceChangeHandler {
	return &outputStreamSourceChangeHandler{
		FFStream: s,
	}
}

func (h *outputStreamSourceChangeHandler) OnStreamSourceChange(
	ctx context.Context,
	pkt packet.Input,
	prevSource packet.Source,
) {
	h.nodeOutput.Flush()
	h.nodeOutput.Processor.Kernel.DeleteStream(ctx, pkt.StreamIndex())
}
