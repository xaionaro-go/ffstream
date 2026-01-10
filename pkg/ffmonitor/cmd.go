package ffmonitor

import (
	"time"

	"github.com/spf13/cobra"
	"github.com/xaionaro-go/avpipeline/monitor"
	avpipeline_proto "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
)

type Config struct {
	EventType            avpipeline_proto.MonitorEventType
	IncludePacketPayload bool
	IncludeFramePayload  bool
	DoDecode             bool
	Format               string
	InputFormat          string
	HighlightMissed      time.Duration
}

func AddFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("include-packet-payload", false, "include packet payloads in monitor events")
	cmd.Flags().Bool("include-frame-payload", false, "include frame payloads in monitor events")
	cmd.Flags().Bool("do-decode", false, "do decode of packets/frames for monitor events")
	cmd.Flags().String("format", "plaintext", "output format (plaintext|json)")
	cmd.Flags().StringP("input-format", "f", "", "force input format")
	cmd.Flags().Duration("highlight-missed", 0, "highlight missed frames (if the gap is greater than the specified duration)")
}

func ParseFlags(cmd *cobra.Command) (Config, error) {
	includePacketPayload, _ := cmd.Flags().GetBool("include-packet-payload")
	includeFramePayload, _ := cmd.Flags().GetBool("include-frame-payload")
	doDecode, _ := cmd.Flags().GetBool("do-decode")
	format, _ := cmd.Flags().GetString("format")
	inputFormat, _ := cmd.Flags().GetString("input-format")
	highlightMissed, _ := cmd.Flags().GetDuration("highlight-missed")

	return Config{
		IncludePacketPayload: includePacketPayload,
		IncludeFramePayload:  includeFramePayload,
		DoDecode:             doDecode,
		Format:               format,
		InputFormat:          inputFormat,
		HighlightMissed:      highlightMissed,
	}, nil
}

func ParseEventType(s string) (avpipeline_proto.MonitorEventType, error) {
	return monitor.ParseEventType(s)
}
