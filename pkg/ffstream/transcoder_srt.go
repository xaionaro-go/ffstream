//go:build with_libsrt
// +build with_libsrt

package ffstream

import (
	"context"
	"fmt"

	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/preset/streammux"
	"github.com/xaionaro-go/avpipeline/processor"
	"github.com/xaionaro-go/libsrt/threadsafe"
	"github.com/xaionaro-go/xsync"
)

func (s *FFStream) WithSRTOutput(
	ctx context.Context,
	outputID int,
	callback func(*threadsafe.Socket) error,
) error {
	output, err := xsync.DoR2(ctx, &s.StreamMux.Locker, func() (*streammux.Output[CustomData], error) {
		output, _ := s.StreamMux.Outputs.Load(streammux.OutputID(outputID))
		if output == nil {
			return nil, fmt.Errorf("output %d is not initialized", outputID)
		}
		return output, nil
	})
	if err != nil {
		return fmt.Errorf("unable to get the output: %w", err)
	}

	procAbstract := output.SendingNode.GetProcessor()
	proc, ok := procAbstract.(processor.GetKerneler)
	if !ok {
		return fmt.Errorf("output %d processor %T does not implement GetKerneler interface", outputID, procAbstract)
	}

	kernelAbstract := proc.GetKernel()
	kernel, ok := kernelAbstract.(kernel.GetSRTer)
	if !ok {
		return fmt.Errorf("output %d kernel %T does not implement GetSRTer interface", outputID, kernelAbstract)
	}

	sock, err := kernel.SRT(ctx)
	if err != nil {
		return fmt.Errorf("unable to get SRT socket: %w", err)
	}

	err = callback(sock)
	if err != nil {
		return fmt.Errorf("callback failed: %w", err)
	}

	return nil
}
