//go:build !with_libsrt
// +build !with_libsrt

package streamforward

import (
	"context"
	"fmt"
)

func (s *StreamForward[C, P]) WithSRTOutput(
	ctx context.Context,
	callback func(any) error,
) error {
	return fmt.Errorf("compiled without with_libsrt")
}
