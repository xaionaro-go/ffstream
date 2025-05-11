//go:build !with_libsrt
// +build !with_libsrt

package ffstream

import (
	"context"
	"fmt"
)

func (s *FFStream) WithSRTOutput(
	ctx context.Context,
	callback func(any) error,
) error {
	return fmt.Errorf("compiled without with_libsrt")
}
