// commands.go defines the commands for ffstreamctl.

// Package commands provides the command-line interface for ffstreamctl.
package commands

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/spf13/cobra"
	"github.com/xaionaro-go/avpipeline/indicator"
	"github.com/xaionaro-go/avpipeline/monitor"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avpipeline_proto "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/buildinfo"
	"github.com/xaionaro-go/ffstream/pkg/ffmonitor"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/client"
	"github.com/xaionaro-go/observability"
	"github.com/xaionaro-go/polyjson"
)

var (
	// Access these variables only from a main package:

	Root = &cobra.Command{
		Use: os.Args[0],
		// Version is the one-line identity rendered by `--version`.
		// Cobra auto-installs the `--version` flag when this field
		// is non-empty (see InitDefaultVersionFlag in cobra).
		Version: buildinfo.VersionString(),
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			ctx := cmd.Context()
			l := logger.FromCtx(ctx).WithLevel(LoggerLevel)
			ctx = logger.CtxWithLogger(ctx, l)
			cmd.SetContext(ctx)
			logger.Debugf(ctx, "log-level: %v", LoggerLevel)

			netPprofAddr, err := cmd.Flags().GetString("go-net-pprof-addr")
			if err != nil {
				l.Error("unable to get the value of the flag 'go-net-pprof-addr': %v", err)
			}
			if netPprofAddr != "" {
				observability.Go(ctx, func(ctx context.Context) {
					if netPprofAddr == "" {
						netPprofAddr = "localhost:0"
					}
					l.Infof("starting to listen for net/pprof requests at '%s'", netPprofAddr)
					l.Error(http.ListenAndServe(netPprofAddr, nil))
				})
			}
		},
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			ctx := cmd.Context()
			logger.Debug(ctx, "end")
		},
	}

	Stats = &cobra.Command{
		Use: "stats",
	}

	StatsEncoder = &cobra.Command{
		Use:  "encoder",
		Args: cobra.ExactArgs(0),
		Run:  statsEncoder,
	}

	StatsBitRates = &cobra.Command{
		Use:  "bitrates",
		Args: cobra.ExactArgs(0),
		Run:  statsBitRates,
	}

	StatsLatencies = &cobra.Command{
		Use:  "latencies",
		Args: cobra.ExactArgs(0),
		Run:  statsLatencies,
	}

	StatsInputQuality = &cobra.Command{
		Use:  "input_quality",
		Args: cobra.ExactArgs(0),
		Run:  statsInputQuality,
	}

	StatsOutputQuality = &cobra.Command{
		Use:  "output_quality",
		Args: cobra.ExactArgs(0),
		Run:  statsOutputQuality,
	}

	StatsFirstFrame = &cobra.Command{
		Use:   "first-frame",
		Short: "Walk the pipeline graph and report per-node first-output timestamps",
		Long: "Reports per-node first-frame timing for cascade-EOF root-cause " +
			"localization. Walks the pipeline graph from each input root and " +
			"prints node_id, type, name, first_frame_at (or 'none' if no " +
			"output observed), age_seconds (since first frame). Nodes whose " +
			"first_frame_unix_ns is still 0 after the grace period are " +
			"highlighted as STALLED — they are the candidate wedge sites.",
		Args: cobra.ExactArgs(0),
		Run:  statsFirstFrame,
	}

	SRT = &cobra.Command{
		Use: "srt",
	}

	SRTFlag = &cobra.Command{
		Use: "flag",
	}

	SRTFlagInt = &cobra.Command{
		Use: "int",
	}

	Encoder = &cobra.Command{
		Use: "encoder",
	}

	EncoderConfig = &cobra.Command{
		Use: "config",
	}

	EncoderAutoBitRate = &cobra.Command{
		Use: "auto_bitrate",
	}

	EncoderAutoBitRateVideo = &cobra.Command{
		Use: "video",
	}

	EncoderAutoBitRateVideoCalculator = &cobra.Command{
		Use: "calculator",
	}

	EncoderAutoBitRateVideoCalculatorGet = &cobra.Command{
		Use:  "get",
		Args: cobra.ExactArgs(0),
		Run:  autoBitRateCalculatorGet,
	}

	EncoderAutoBitRateVideoCalculatorSet = &cobra.Command{
		Use:  "set",
		Args: cobra.ExactArgs(0),
		Run:  autoBitRateCalculatorSet,
	}

	EncoderAutoBitRateVideoConfig = &cobra.Command{
		Use: "config",
	}

	EncoderAutoBitRateVideoConfigGet = &cobra.Command{
		Use:  "get",
		Args: cobra.ExactArgs(0),
		Run:  autoBitRateConfigGet,
	}

	EncoderAutoBitRateVideoConfigSet = &cobra.Command{
		Use:  "set",
		Args: cobra.ExactArgs(0),
		Run:  autoBitRateConfigSet,
	}

	EncoderReinit = &cobra.Command{
		Use:   "reinit",
		Short: "trigger an explicit close+reopen of the active video encoder (for instrumented canary measurement)",
		Args:  cobra.ExactArgs(0),
		Run:   encoderReinit,
	}

	EncoderFPSFraction = &cobra.Command{
		Use: "fps_fraction",
	}

	EncoderFPSFractionGet = &cobra.Command{
		Use:  "get",
		Args: cobra.ExactArgs(0),
		Run:  encoderFPSFractionGet,
	}

	EncoderFPSFractionSet = &cobra.Command{
		Use:  "set",
		Args: cobra.ExactArgs(2),
		Run:  encoderFPSFractionSet,
	}

	Buffer = &cobra.Command{
		Use: "buffer",
	}

	BufferOutput = &cobra.Command{
		Use: "output",
	}

	LoggerLevel = logger.LevelWarning

	Pipelines = &cobra.Command{
		Use: "pipelines",
	}

	PipelinesGet = &cobra.Command{
		Use:  "get",
		Args: cobra.ExactArgs(0),
		Run:  pipelinesGet,
	}

	Monitor = &cobra.Command{
		Use:  "monitor",
		Args: cobra.RangeArgs(1, 2),
		Run:  monitorCommand,
	}

	Inputs = &cobra.Command{
		Use: "inputs",
	}

	InputsInfo = &cobra.Command{
		Use:  "info",
		Args: cobra.ExactArgs(0),
		Run:  inputsInfo,
	}

	InputsSetCustomOption = &cobra.Command{
		Use:  "set_custom_option <input_priority> <input_num> <key> <value>",
		Args: cobra.ExactArgs(4),
		Run:  inputsSetCustomOption,
	}

	InputsSetStop = &cobra.Command{
		Use:  "set_stop <input_priority> <stop>",
		Args: cobra.ExactArgs(2),
		Run:  inputsSetStop,
	}

	InputsAdd = &cobra.Command{
		Use:  "add <priority> <url>",
		Args: cobra.ExactArgs(2),
		Run:  inputsAdd,
	}

	InputsRemove = &cobra.Command{
		Use:  "remove <priority> <num>",
		Args: cobra.ExactArgs(2),
		Run:  inputsRemove,
	}

	Output = &cobra.Command{
		Use: "output",
	}

	OutputSwitch = &cobra.Command{
		Use:  "switch <video_codec> <video_width> <video_height> <video_bitrate> <audio_codec> <audio_sample_rate> <audio_bitrate> <max_bitrate>",
		Args: cobra.ExactArgs(8),
		Run:  outputSwitch,
	}

	OutputSetURL = &cobra.Command{
		Use:  "set-url <url>",
		Args: cobra.ExactArgs(1),
		Run:  outputSetURL,
	}

	InjectSubtitles = &cobra.Command{
		Use:  "inject_subtitles <text>",
		Args: cobra.ExactArgs(1),
		Run:  injectSubtitles,
	}

	InjectData = &cobra.Command{
		Use:  "inject_data <hex_data>",
		Args: cobra.ExactArgs(1),
		Run:  injectData,
	}
)

func init() {
	// Render --version as the same indented JSON ffstream emits, so
	// both binaries' --version output is byte-for-byte comparable.
	// {{printf "%s"}} suppresses text/template's HTML-escape of the
	// JSON `<`/`>`/`&` it would otherwise apply.
	Root.SetVersionTemplate("{{printf \"%s\" .Annotations.versionJSON}}")

	versionJSON, err := buildinfo.JSON()
	if err != nil {
		// Fall back to the one-line VersionString rather than failing
		// the binary; --version must never crash a CLI tool.
		versionJSON = []byte(buildinfo.VersionString() + "\n")
	}
	if Root.Annotations == nil {
		Root.Annotations = map[string]string{}
	}
	Root.Annotations["versionJSON"] = string(versionJSON)

	Root.AddCommand(Stats)
	Stats.AddCommand(StatsEncoder)
	Stats.AddCommand(StatsBitRates)
	Stats.AddCommand(StatsLatencies)
	Stats.AddCommand(StatsInputQuality)
	Stats.AddCommand(StatsOutputQuality)
	Stats.AddCommand(StatsFirstFrame)
	StatsFirstFrame.Flags().Duration("grace", 5*time.Second, "highlight nodes whose first_frame_unix_ns is still 0 after this grace period elapsed since daemon start (only used for the STALLED label; output always includes all nodes)")

	Root.AddCommand(Encoder)
	Encoder.AddCommand(EncoderConfig)

	Encoder.AddCommand(EncoderAutoBitRate)
	EncoderAutoBitRate.AddCommand(EncoderAutoBitRateVideo)
	EncoderAutoBitRateVideo.AddCommand(EncoderAutoBitRateVideoCalculator)
	EncoderAutoBitRateVideoCalculator.AddCommand(EncoderAutoBitRateVideoCalculatorGet)
	EncoderAutoBitRateVideoCalculator.AddCommand(EncoderAutoBitRateVideoCalculatorSet)
	EncoderAutoBitRateVideo.AddCommand(EncoderAutoBitRateVideoConfig)
	EncoderAutoBitRateVideoConfig.AddCommand(EncoderAutoBitRateVideoConfigGet)
	EncoderAutoBitRateVideoConfig.AddCommand(EncoderAutoBitRateVideoConfigSet)

	Encoder.AddCommand(EncoderFPSFraction)
	EncoderFPSFraction.AddCommand(EncoderFPSFractionGet)
	EncoderFPSFraction.AddCommand(EncoderFPSFractionSet)

	Encoder.AddCommand(EncoderReinit)

	Root.PersistentFlags().Var(&LoggerLevel, "log-level", "")
	Root.PersistentFlags().String("remote-addr", "localhost:3594", "the address to an ffstream instance")
	Root.PersistentFlags().String("go-net-pprof-addr", "", "address to listen to for net/pprof requests")

	StatsEncoder.PersistentFlags().String("title", "", "stream title")
	StatsEncoder.PersistentFlags().String("description", "", "stream description")
	StatsEncoder.PersistentFlags().String("profile", "", "profile")

	Root.AddCommand(Buffer)
	Buffer.AddCommand(BufferOutput)

	Root.AddCommand(Pipelines)
	Pipelines.AddCommand(PipelinesGet)

	Root.AddCommand(Monitor)
	ffmonitor.AddFlags(Monitor)

	Root.AddCommand(Inputs)
	Inputs.AddCommand(InputsInfo)
	Inputs.AddCommand(InputsSetCustomOption)
	Inputs.AddCommand(InputsSetStop)
	InputsAdd.Flags().StringSlice("custom-option", nil, "custom input option (key=value); may be repeated")
	Inputs.AddCommand(InputsAdd)
	Inputs.AddCommand(InputsRemove)

	Root.AddCommand(Output)
	Output.AddCommand(OutputSwitch)
	Output.AddCommand(OutputSetURL)

	Root.AddCommand(InjectSubtitles)
	InjectSubtitles.Flags().Duration("duration", time.Second, "the duration of the subtitle")

	Root.AddCommand(InjectData)
	InjectData.Flags().Duration("duration", time.Second, "the duration of the data")

	polyjson.AutoRegisterTypes = true
	polyjson.RegisterType(streammuxtypes.AutoBitrateCalculatorThresholds{})
	polyjson.RegisterType(streammuxtypes.AutoBitrateCalculatorLogK{})
	polyjson.RegisterType(streammuxtypes.AutoBitrateCalculatorStatic(0))
	polyjson.RegisterType(streammuxtypes.AutoBitrateCalculatorQueueSizeGapDecay{})
	polyjson.RegisterType(streammuxtypes.UBps(0))
	polyjson.RegisterType(indicator.MAMA[float64]{})
	polyjson.RegisterType(indicator.MAMA[streammuxtypes.UBps]{})
}

func assertNoError(ctx context.Context, err error) {
	if err != nil {
		logger.Panic(ctx, err)
	}
}

func statsEncoder(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	stats, err := client.GetStats(ctx)
	assertNoError(ctx, err)

	jsonOutput(ctx, cmd.OutOrStdout(), stats)
}

func statsBitRates(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	bitRates, err := client.GetBitRates(ctx)
	assertNoError(ctx, err)

	jsonOutput(ctx, cmd.OutOrStdout(), bitRates)
}

func statsLatencies(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	latencies, err := client.GetLatencies(ctx)
	assertNoError(ctx, err)

	jsonOutput(ctx, cmd.OutOrStdout(), latencies)
}

func statsInputQuality(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	inputQuality, err := client.GetInputQuality(ctx)
	assertNoError(ctx, err)

	jsonOutput(ctx, cmd.OutOrStdout(), inputQuality)
}

func statsOutputQuality(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	outputQuality, err := client.GetOutputQuality(ctx)
	assertNoError(ctx, err)

	jsonOutput(ctx, cmd.OutOrStdout(), outputQuality)
}

// encoderFPSFractionGet calls the server and prints "num den\n"
func encoderFPSFractionGet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	c := client.New(remoteAddr)

	// expecting client.GetFPSFraction(ctx) to return (num uint32, den uint32, err error)
	num, den, err := c.GetFPSFraction(ctx)
	assertNoError(ctx, err)

	fmt.Fprintf(cmd.OutOrStdout(), "%d %d\n", num, den)
}

// encoderFPSFractionSet parses two integers (num den) and sends them to the server
func encoderFPSFractionSet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	num64, err := strconv.ParseUint(args[0], 10, 32)
	assertNoError(ctx, err)
	den64, err := strconv.ParseUint(args[1], 10, 32)
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	c := client.New(remoteAddr)

	// expecting client.SetFPSFraction(ctx, num uint32, den uint32) error
	err = c.SetFPSFraction(ctx, uint32(num64), uint32(den64))
	assertNoError(ctx, err)
}

// encoderReinit calls the server-side ReinitEncoder RPC and prints the
// reported close+open duration in milliseconds (with microsecond
// precision). The duration is the server-side wall-clock measurement
// of the codec context close+open and excludes RPC overhead.
func encoderReinit(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	c := client.New(remoteAddr)

	clientStart := time.Now()
	dur, err := c.ReinitEncoder(ctx)
	clientElapsed := time.Since(clientStart)
	assertNoError(ctx, err)

	fmt.Fprintf(
		cmd.OutOrStdout(),
		"server_reinit_us=%d client_roundtrip_us=%d\n",
		dur.Microseconds(),
		clientElapsed.Microseconds(),
	)
}

func pipelinesGet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	pipelines, err := client.GetPipelines(ctx)
	assertNoError(ctx, err)

	jsonOutput(ctx, cmd.OutOrStdout(), pipelines)
}

// statsFirstFrame walks the pipeline graph (returned by GetPipelines)
// and prints per-node first-frame timing. Nodes whose first-frame
// timestamp is still 0 after the configured grace period are marked
// STALLED — they are the candidate cascade-EOF wedge sites. See
// StatsFirstFrame.Long for the full operator workflow.
//
// Output format (tab-separated, one node per line):
//
//	<node_id>\t<status>\t<type>\t<first_frame_at_or_none>\t<age>\t<description>
//
// where status is "OK" for nodes with first_frame_unix_ns != 0 and
// "STALLED" or "PENDING" for nodes that haven't emitted yet.
func statsFirstFrame(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	grace, err := cmd.Flags().GetDuration("grace")
	assertNoError(ctx, err)

	c := client.New(remoteAddr)

	pipelines, err := c.GetPipelines(ctx)
	assertNoError(ctx, err)

	now := time.Now()
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "node_id\tstatus\ttype\tfirst_frame_at\tage\tdescription")

	walked := map[uint64]struct{}{}
	for _, root := range pipelines.GetNodes() {
		walkFirstFrameNode(out, root, now, grace, walked)
	}
}

// walkFirstFrameNode is a depth-first traverse that reports each node
// once (avoiding loops via the walked set keyed by node_id).
func walkFirstFrameNode(
	out io.Writer,
	n *avpipeline_proto.Node,
	now time.Time,
	grace time.Duration,
	walked map[uint64]struct{},
) {
	if n == nil {
		return
	}
	if _, seen := walked[n.GetId()]; seen {
		return
	}
	walked[n.GetId()] = struct{}{}

	ffUnixNs := n.GetFirstFrameUnixNs()
	var status, firstFrameAtStr, ageStr string
	switch {
	case ffUnixNs > 0:
		ts := time.Unix(0, ffUnixNs)
		status = "OK"
		firstFrameAtStr = ts.UTC().Format(time.RFC3339Nano)
		ageStr = now.Sub(ts).Truncate(time.Millisecond).String()
	default:
		// We don't have the daemon's start time here, so use the
		// flag-supplied grace period as a proxy: if the operator
		// believes the daemon has been up at least `grace`, any
		// FromKernel processor that still has 0 is a wedge candidate.
		// Operators run this command after a settling period; the
		// PENDING-vs-STALLED distinction is informational.
		_ = grace
		status = "STALLED"
		firstFrameAtStr = "none"
		ageStr = "-"
	}

	fmt.Fprintf(out, "%d\t%s\t%s\t%s\t%s\t%s\n",
		n.GetId(),
		status,
		n.GetType(),
		firstFrameAtStr,
		ageStr,
		n.GetDescription(),
	)

	for _, child := range n.GetConsumingNodes() {
		walkFirstFrameNode(out, child, now, grace, walked)
	}
}

func autoBitRateCalculatorGet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	calculator, err := client.GetVideoAutoBitRateCalculator(ctx)
	assertNoError(ctx, err)
	logger.Debugf(ctx, "got AutoBitRateCalculator: %#v", calculator)

	m := map[string]streammuxtypes.AutoBitRateCalculator{
		"calculator": calculator,
	}

	b, err := polyjson.MarshalWithTypeIDs(m, polyjson.TypeRegistry())
	assertNoError(ctx, err)

	// a workaround for a bug in polyjson:
	m2 := map[string]json.RawMessage{}
	err = json.Unmarshal(b, &m2)
	assertNoError(ctx, err)

	cmd.OutOrStdout().Write(m2["calculator"])
}

func autoBitRateCalculatorSet(cmd *cobra.Command, args []string) {
	// an example:
	// echo '{"./avpipeline/preset/streammux/types.AutoBitrateCalculatorStatic":1000}' | ffstreamctl encoder auto_bitrate calculator set
	ctx := cmd.Context()

	b, err := io.ReadAll(cmd.InOrStdin())
	assertNoError(ctx, err)

	var m map[string]streammuxtypes.AutoBitRateCalculator
	err = polyjson.UnmarshalWithTypeIDs([]byte(`{"calculator":`+string(b)+`}`), &m, polyjson.TypeRegistry())
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	logger.Debugf(ctx, "setting AutoBitRateCalculator: %#v", m["calculator"])
	err = client.SetVideoAutoBitRateCalculator(ctx, m["calculator"])
	assertNoError(ctx, err)
}

func autoBitRateConfigGet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	cfg, err := client.GetVideoAutoBitRateConfig(ctx)
	assertNoError(ctx, err)
	logger.Debugf(ctx, "got AutoBitRateConfig: %#v", cfg)

	m := map[string]*streammuxtypes.AutoBitRateVideoConfig{
		"config": cfg,
	}

	b, err := polyjson.MarshalWithTypeIDs(m, polyjson.TypeRegistry())
	assertNoError(ctx, err)

	// a workaround for a bug in polyjson:
	m2 := map[string]json.RawMessage{}
	err = json.Unmarshal(b, &m2)
	assertNoError(ctx, err)
	b64 := strings.Trim(string(m2["config"]), `"`)
	j, err := base64.StdEncoding.DecodeString(string(b64))
	assertNoError(ctx, err)

	cmd.OutOrStdout().Write(j)
}

func autoBitRateConfigSet(cmd *cobra.Command, args []string) {
	// an example:
	// echo '{"./avpipeline/preset/streammux/types.AutoBitrateConfigStatic":1000}' | ffstreamctl encoder auto_bitrate calculator set
	ctx := cmd.Context()

	b, err := io.ReadAll(cmd.InOrStdin())
	assertNoError(ctx, err)

	var m map[string]*streammuxtypes.AutoBitRateVideoConfig
	err = polyjson.UnmarshalWithTypeIDs([]byte(`{"config":`+string(b)+`}`), &m, polyjson.TypeRegistry())
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	logger.Debugf(ctx, "setting AutoBitRateConfig: %#v", m["config"])
	err = client.SetVideoAutoBitRateConfig(ctx, m["config"])
	assertNoError(ctx, err)
}

func monitorCommand(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	objID, err := strconv.ParseUint(args[0], 10, 64)
	assertNoError(ctx, err)

	evenType := avpipeline_proto.MonitorEventType_EVENT_TYPE_SEND
	if len(args) >= 2 {
		var err error
		evenType, err = ffmonitor.ParseEventType(args[1])
		assertNoError(ctx, err)
	}

	mcfg, err := ffmonitor.ParseFlags(cmd)
	assertNoError(ctx, err)

	eventsCh, err := client.Monitor(ctx, objID, evenType, mcfg.IncludePacketPayload, mcfg.IncludeFramePayload, mcfg.DoDecode)
	assertNoError(ctx, err)

	logger.Infof(ctx, "monitoring started for object ID %d, event type %s", objID, evenType.String())
	err = ffmonitor.PrintMonitorEvents(ctx, eventsCh, monitor.PrintOptions{
		Format:                 mcfg.Format,
		HighlightDiscontinuity: mcfg.HighlightDiscontinuity,
		StreamIndices:          mcfg.StreamIndices,
	})
	assertNoError(ctx, err)
}

func inputsInfo(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	inputsInfo, err := client.GetInputsInfo(ctx)
	assertNoError(ctx, err)

	jsonOutput(ctx, cmd.OutOrStdout(), inputsInfo)
}

func inputsSetCustomOption(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	inputPriority, err := strconv.ParseUint(args[0], 10, 32)
	assertNoError(ctx, err)

	inputNum, err := strconv.ParseUint(args[1], 10, 32)
	assertNoError(ctx, err)

	key := args[2]
	value := args[3]

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	err = client.SetInputCustomOption(ctx, inputPriority, inputNum, avptypes.DictionaryItem{
		Key:   key,
		Value: value,
	})
	assertNoError(ctx, err)
}

func inputsSetStop(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	inputPriority, err := strconv.ParseUint(args[0], 10, 32)
	assertNoError(ctx, err)

	stop, err := strconv.ParseBool(args[1])
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	err = client.SetStopInput(ctx, inputPriority, stop)
	assertNoError(ctx, err)
}

func inputsAdd(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	priority, err := strconv.ParseUint(args[0], 10, 64)
	assertNoError(ctx, err)
	inputURL := args[1]

	rawOpts, err := cmd.Flags().GetStringSlice("custom-option")
	assertNoError(ctx, err)

	var customOpts []*avpipeline_proto.CustomOption
	for _, kv := range rawOpts {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			logger.Panicf(ctx, "invalid custom option %q (expected key=value)", kv)
		}
		customOpts = append(customOpts, &avpipeline_proto.CustomOption{Key: k, Value: v})
	}

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	c := client.New(remoteAddr)

	num, err := c.AddInput(ctx, priority, inputURL, &avpipeline_proto.InputConfig{CustomOptions: customOpts})
	assertNoError(ctx, err)

	logger.Infof(ctx, "added input at (priority=%d, num=%d)", priority, num)
}

func inputsRemove(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	priority, err := strconv.ParseUint(args[0], 10, 64)
	assertNoError(ctx, err)
	num, err := strconv.ParseUint(args[1], 10, 64)
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	c := client.New(remoteAddr)

	err = c.RemoveInput(ctx, priority, num)
	assertNoError(ctx, err)
}

func outputSwitch(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	videoCodecName := args[0]
	videoWidth, err := strconv.ParseUint(args[1], 10, 32)
	assertNoError(ctx, err)
	videoHeight, err := strconv.ParseUint(args[2], 10, 32)
	assertNoError(ctx, err)
	videoAvgBitRate, err := strconv.ParseUint(args[3], 10, 64)
	assertNoError(ctx, err)
	audioCodecName := args[4]
	audioSampleRate, err := strconv.ParseUint(args[5], 10, 32)
	assertNoError(ctx, err)
	audioAvgBitRate, err := strconv.ParseUint(args[6], 10, 64)
	assertNoError(ctx, err)
	maxBitRate, err := strconv.ParseUint(args[7], 10, 64)
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	logger.Infof(ctx, "switching output: video=%s@%dx%d:%d audio=%s@%d:%d max=%d",
		videoCodecName, videoWidth, videoHeight, videoAvgBitRate,
		audioCodecName, audioSampleRate, audioAvgBitRate, maxBitRate)

	err = client.SwitchOutputByProps(ctx,
		videoCodecName, uint32(videoWidth), uint32(videoHeight), videoAvgBitRate,
		audioCodecName, uint32(audioSampleRate), audioAvgBitRate, maxBitRate)
	assertNoError(ctx, err)

	logger.Infof(ctx, "output switch completed successfully")
}

func outputSetURL(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	url := args[0]

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	c := client.New(remoteAddr)

	logger.Infof(ctx, "setting output URL: %q", url)
	err = c.SetOutputURL(ctx, url)
	assertNoError(ctx, err)
	logger.Infof(ctx, "output URL set successfully")
}

func injectSubtitles(cmd *cobra.Command, args []string) {
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	text := args[0]

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	duration, err := cmd.Flags().GetDuration("duration")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	err = client.InjectSubtitles(ctx, text, duration)
	assertNoError(ctx, err)
}

func injectData(cmd *cobra.Command, args []string) {
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	hexData := args[0]
	data, err := hex.DecodeString(hexData)
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	duration, err := cmd.Flags().GetDuration("duration")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	err = client.InjectData(ctx, data, duration)
	assertNoError(ctx, err)
}
