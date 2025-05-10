//go:build with_libsrt
// +build with_libsrt

package streamforward

import (
	"context"
	"fmt"

	"github.com/xaionaro-go/libsrt/threadsafe"
)

func (s *StreamForward) WithSRTOutput(
	ctx context.Context,
	callback func(*threadsafe.Socket) error,
) error {
	sock, err := s.Output.SRT(ctx)
	if err != nil {
		return fmt.Errorf("unable to get the SRT socket handler: %w", err)
	}

	return callback(sock)
}
