package ffmonitor

import (
	"context"
	"os"

	"github.com/xaionaro-go/avpipeline/monitor"
	avpipeline_proto "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
)

func PrintMonitorEvents(ctx context.Context, eventsCh <-chan *avpipeline_proto.MonitorEvent, format string) error {
	return monitor.PrintMonitorEvents(ctx, os.Stdout, eventsCh, format)
}
