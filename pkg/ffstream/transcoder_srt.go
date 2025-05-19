//go:build with_libsrt
// +build with_libsrt

package ffstream

import (
	"context"
	"fmt"

	"github.com/xaionaro-go/libsrt/threadsafe"
)

func (s *FFStream) WithSRTOutput(
	ctx context.Context,
	callback func(*threadsafe.Socket) error,
) error {
	// TODO: add support for multiple outputs
	sock, err := s.NodeOutputs[0].Processor.Kernel.SRT(ctx)
	if err != nil {
		return fmt.Errorf("unable to get the SRT socket handler: %w", err)
	}

	return callback(sock)
}
