package ffstream

import (
	"context"
	"fmt"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/node"
	"github.com/xaionaro-go/avpipeline/preset/streammux"
	"github.com/xaionaro-go/avpipeline/processor"
	"github.com/xaionaro-go/xsync"
)

type nodeSetDropOnCloserWrapper struct {
	*node.NodeWithCustomData[streammux.OutputCustomData[CustomData], *processor.FromKernel[*kernel.Output]]
}

var _ SendingNode = (*nodeSetDropOnCloserWrapper)(nil)

func (n nodeSetDropOnCloserWrapper) SetDropOnClose(
	ctx context.Context,
	dropOnClose bool,
) (_err error) {
	logger.Debugf(ctx, "SetDropOnClose(ctx, %v)", dropOnClose)
	defer func() { logger.Debugf(ctx, "/SetDropOnClose(ctx, %v): %v", dropOnClose, _err) }()
	onOff := int32(0)
	if dropOnClose {
		onOff = 1
	}
	return n.Processor.Kernel.UnsafeSetLinger(ctx, onOff, 0)
}

type nodeWithRetrySetDropOnCloserWrapper struct {
	*node.NodeWithCustomData[streammux.OutputCustomData[CustomData], *processor.FromKernel[*kernel.Retry[*kernel.Output]]]
}

var _ SendingNode = (*nodeWithRetrySetDropOnCloserWrapper)(nil)

func (n nodeWithRetrySetDropOnCloserWrapper) SetDropOnClose(
	ctx context.Context,
	dropOnClose bool,
) (_err error) {
	logger.Debugf(ctx, "SetDropOnClose(ctx, %v)", dropOnClose)
	defer func() { logger.Debugf(ctx, "/SetDropOnClose(ctx, %v): %v", dropOnClose, _err) }()
	onOff := int32(0)
	if dropOnClose {
		onOff = 1
	}
	retryKernel := n.Processor.Kernel
	return xsync.DoR1(ctx, &retryKernel.KernelLocker, func() error {
		k := retryKernel.Kernel
		if k == nil {
			return fmt.Errorf("underlying kernel is nil")
		}
		err := k.UnsafeSetLinger(ctx, onOff, 0)
		if err != nil {
			return fmt.Errorf("failed to set drop on close to %v: %w", dropOnClose, err)
		}
		return nil
	})
}
