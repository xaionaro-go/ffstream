//go:build !libsrt
// +build !libsrt

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
	return fmt.Errorf("compiled without libsrt")
}
