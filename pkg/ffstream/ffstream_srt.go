//go:build libsrt
// +build libsrt

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
	sock, err := s.Output.SRT(ctx)
	if err != nil {
		return fmt.Errorf("unable to get the SRT socket handler: %w", err)
	}

	return callback(sock)
}
