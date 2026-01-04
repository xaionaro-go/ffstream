// set_drop_on_close.go provides wrappers to set socket options on output nodes to drop data on close.

package ffstream

import (
	"context"
	"fmt"
	"syscall"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/kernel"
	netraw "github.com/xaionaro-go/avpipeline/net/raw"
	"github.com/xaionaro-go/avpipeline/node"
	"github.com/xaionaro-go/avpipeline/preset/streammux"
	"github.com/xaionaro-go/avpipeline/processor"
	tcpopt "github.com/xaionaro-go/tcp/opt"
	"github.com/xaionaro-go/xsync"
)

type SendingNode = node.NodeWithCustomData[streammux.OutputCustomData[CustomData], *processor.FromKernel[*kernel.Output]]

type nodeSetDropOnCloserWrapper struct {
	*SendingNode
}

var _ SendingNodeAbstract = (*nodeSetDropOnCloserWrapper)(nil)

func (n nodeSetDropOnCloserWrapper) SetDropOnClose(
	ctx context.Context,
	dropOnClose bool,
) (_err error) {
	logger.Debugf(ctx, "SetDropOnClose(ctx, %v)", dropOnClose)
	defer func() { logger.Debugf(ctx, "/SetDropOnClose(ctx, %v): %v", dropOnClose, _err) }()

	err := n.Processor.Kernel.UnsafeWithRawNetworkConn(ctx, func(
		ctx context.Context,
		rc syscall.RawConn,
		s string,
	) error {
		return netraw.SetOption(ctx, rc, tcpopt.Linger{
			OnOff: dropOnClose,
		})
	})
	if err != nil {
		return fmt.Errorf("failed to set drop on close to %v: %w", dropOnClose, err)
	}
	return nil
}

func (n nodeSetDropOnCloserWrapper) String() string {
	return fmt.Sprintf("SetDropOnCloserWrapper(%s)", n.OriginalNode())
}

func (n nodeSetDropOnCloserWrapper) OriginalNode() *SendingNode {
	return n.SendingNode
}

func (n nodeSetDropOnCloserWrapper) OriginalNodeAbstract() node.Abstract {
	r := n.OriginalNode()
	if r == nil {
		return nil
	}
	return r
}

type SendingNodeWithRetry = node.NodeWithCustomData[streammux.OutputCustomData[CustomData], *processor.FromKernel[*kernel.Retryable[*kernel.Output]]]

type nodeWithRetrySetDropOnCloserWrapper struct {
	*SendingNodeWithRetry
}

var _ SendingNodeAbstract = (*nodeWithRetrySetDropOnCloserWrapper)(nil)

func (n nodeWithRetrySetDropOnCloserWrapper) SetDropOnClose(
	ctx context.Context,
	dropOnClose bool,
) (_err error) {
	logger.Debugf(ctx, "SetDropOnClose(ctx, %v)", dropOnClose)
	defer func() { logger.Debugf(ctx, "/SetDropOnClose(ctx, %v): %v", dropOnClose, _err) }()
	retryKernel := n.Processor.Kernel
	return xsync.DoR1(ctx, &retryKernel.KernelLocker, func() error {
		k := retryKernel.Kernel
		if k == nil {
			return fmt.Errorf("underlying kernel is nil")
		}
		err := k.UnsafeWithRawNetworkConn(ctx, func(
			ctx context.Context,
			rc syscall.RawConn,
			s string,
		) error {
			return netraw.SetOption(ctx, rc, tcpopt.Linger{
				OnOff: dropOnClose,
			})
		})
		if err != nil {
			return fmt.Errorf("failed to set drop on close to %v: %w", dropOnClose, err)
		}
		return nil
	})
}

func (n nodeWithRetrySetDropOnCloserWrapper) String() string {
	return fmt.Sprintf("SetDropOnCloserWrapper(%s)", n.OriginalNode())
}

func (n nodeWithRetrySetDropOnCloserWrapper) OriginalNode() *SendingNodeWithRetry {
	return n.SendingNodeWithRetry
}

func (n nodeWithRetrySetDropOnCloserWrapper) OriginalNodeAbstract() node.Abstract {
	r := n.OriginalNode()
	if r == nil {
		return nil
	}
	return r
}
