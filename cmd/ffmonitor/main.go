package main

import (
	"os"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/spf13/cobra"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/monitor"
	"github.com/xaionaro-go/avpipeline/node"
	avpipeline_proto "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
	globaltypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffmonitor"
	"github.com/xaionaro-go/secret"
)

var (
	Root = &cobra.Command{
		Use:  "ffmonitor <url> [event_type]",
		Args: cobra.MinimumNArgs(1),
		Run:  run,
	}
	LoggerLevel = logger.LevelWarning
)

func init() {
	Root.PersistentFlags().Var(&LoggerLevel, "log-level", "")
	ffmonitor.AddFlags(Root)
}

func run(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()
	l := logger.FromCtx(ctx).WithLevel(LoggerLevel)
	ctx = logger.CtxWithLogger(ctx, l)

	url := args[0]
	eventType := avpipeline_proto.MonitorEventType_EVENT_TYPE_SEND
	if len(args) >= 2 {
		var err error
		eventType, err = ffmonitor.ParseEventType(args[1])
		if err != nil {
			logger.Fatalf(ctx, "%v", err)
		}
	}

	mcfg, err := ffmonitor.ParseFlags(cmd)
	if err != nil {
		logger.Fatalf(ctx, "failed to parse flags: %v", err)
	}

	cfg := kernel.InputConfig{}
	if mcfg.InputFormat != "" {
		logger.Debugf(ctx, "forcing input format to %q", mcfg.InputFormat)
		cfg.CustomOptions = globaltypes.DictionaryItems{{Key: "f", Value: mcfg.InputFormat}}
	}

	in, err := kernel.NewInputFromURL(ctx, url, secret.New(""), cfg)
	if err != nil {
		logger.Fatalf(ctx, "unable to create input from URL %q: %v", url, err)
	}

	n := node.NewFromKernel(ctx, in)
	m, err := monitor.New(ctx, n, eventType, mcfg.IncludePacketPayload, mcfg.IncludeFramePayload, mcfg.DoDecode)
	if err != nil {
		logger.Fatalf(ctx, "failed to create monitor: %v", err)
	}

	go func() {
		n.Serve(ctx, node.ServeConfig{}, nil)
	}()

	err = ffmonitor.PrintMonitorEvents(ctx, m.Events, monitor.PrintOptions{
		Format:          mcfg.Format,
		HighlightMissed: mcfg.HighlightMissed,
	})
	if err != nil {
		logger.Fatalf(ctx, "failed to print monitor events: %v", err)
	}
}

func main() {
	if err := Root.Execute(); err != nil {
		os.Exit(1)
	}
}
