package ffstreamserver

import (
	"context"
	"fmt"

	"github.com/xaionaro-go/avpipeline"
	"github.com/xaionaro-go/avpipeline/logger"
	"github.com/xaionaro-go/avpipeline/monitor"
	"github.com/xaionaro-go/avpipeline/node"
	avpipeline_proto "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/xgrpc"
)

type Monitor struct {
	Object node.Abstract
	Events chan *avpipeline_proto.MonitorEvent
	Type   avpipeline_proto.MonitorEventType
}

func (srv *GRPCServer) Monitor(
	req *avpipeline_proto.MonitorRequest,
	reqSrv ffstream_grpc.FFStream_MonitorServer,
) (_err error) {
	ctx := srv.ctx(reqSrv.Context())
	logger.Debugf(ctx, "Monitor: %+v", req)
	defer func() { logger.Debugf(ctx, "/Monitor: %+v: %v", req, _err) }()
	obj := avptypes.ObjectID(req.GetNodeId())
	pipeline := srv.getPipeline(ctx)
	node, err := avpipeline.FindNodeByObjectID(ctx, obj, pipeline...)
	if err != nil {
		return fmt.Errorf("failed to find node by ID %v: %w", obj, err)
	}
	monitor, err := monitor.New(ctx, node, req.GetEventType(), req.GetIncludePacketPayload(), req.GetIncludeFramePayload(), req.GetDoDecode())
	if err != nil {
		return fmt.Errorf("failed to create monitor for node %q: %w", obj, err)
	}
	defer func() {
		err := monitor.Close(ctx)
		if err != nil {
			logger.Errorf(ctx, "failed to close monitor for node %q: %v", obj, err)
		}
	}()
	return xgrpc.WrapChan(ctx,
		func(ctx context.Context) (<-chan *avpipeline_proto.MonitorEvent, error) {
			return monitor.Events, nil
		},
		reqSrv,
		func(in *avpipeline_proto.MonitorEvent) *avpipeline_proto.MonitorEvent {
			return in
		},
	)
}

func (srv *GRPCServer) getPipeline(
	ctx context.Context,
) (result []node.Abstract) {
	return []node.Abstract{
		srv.FFStream.NodeInput,
	}
}
