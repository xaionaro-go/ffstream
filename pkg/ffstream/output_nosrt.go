//go:build !with_libsrt
// +build !with_libsrt

// transcoder_nosrt.go is a stub implementation of SRT features for builds without libsrt.

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
