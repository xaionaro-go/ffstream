package commands

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strconv"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/spf13/cobra"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/client"
	"github.com/xaionaro-go/observability"
)

var (
	// Access these variables only from a main package:

	Root = &cobra.Command{
		Use: os.Args[0],
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

	SRT = &cobra.Command{
		Use: "srt",
	}

	SRTFlag = &cobra.Command{
		Use: "flag",
	}

	SRTFlagInt = &cobra.Command{
		Use: "int",
	}

	EncoderConfig = &cobra.Command{
		Use: "encoder_config",
	}

	EncoderConfigGet = &cobra.Command{
		Use:  "get",
		Args: cobra.ExactArgs(0),
		Run:  encoderConfigGet,
	}

	EncoderConfigSet = &cobra.Command{
		Use:  "set",
		Args: cobra.ExactArgs(0),
		Run:  encoderConfigSet,
	}

	// New FPS divider commands
	EncoderFPSDivider = &cobra.Command{
		Use: "fps_divider",
	}

	EncoderFPSDividerGet = &cobra.Command{
		Use:  "get",
		Args: cobra.ExactArgs(0),
		Run:  encoderFPSDividerGet,
	}

	EncoderFPSDividerSet = &cobra.Command{
		Use:  "set",
		Args: cobra.ExactArgs(2),
		Run:  encoderFPSDividerSet,
	}

	Buffer = &cobra.Command{
		Use: "buffer",
	}

	BufferOutput = &cobra.Command{
		Use: "output",
	}

	BufferOutputTolerable = &cobra.Command{
		Use: "tolerable",
	}

	BufferOutputTolerableGet = &cobra.Command{
		Use:  "get",
		Args: cobra.ExactArgs(0),
		Run:  bufferOutputTolerableGet,
	}

	BufferOutputTolerableSet = &cobra.Command{
		Use:  "set",
		Args: cobra.ExactArgs(1),
		Run:  bufferOutputTolerableSet,
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

	AutoBitRate = &cobra.Command{
		Use: "auto_bitrate",
	}

	AutoBitRateCalculator = &cobra.Command{
		Use: "calculator",
	}

	AutoBitRateCalculatorGet = &cobra.Command{
		Use:  "get",
		Args: cobra.ExactArgs(0),
		Run:  autoBitRateCalculatorGet,
	}

	AutoBitRateCalculatorSet = &cobra.Command{
		Use:  "set",
		Args: cobra.ExactArgs(0),
		Run:  autoBitRateCalculatorSet,
	}
)

func init() {
	Root.AddCommand(Stats)
	Stats.AddCommand(StatsEncoder)

	Root.AddCommand(EncoderConfig)
	EncoderConfig.AddCommand(EncoderConfigGet)
	EncoderConfig.AddCommand(EncoderConfigSet)

	// Register new fps_divider commands under encoder_config
	EncoderConfig.AddCommand(EncoderFPSDivider)
	EncoderFPSDivider.AddCommand(EncoderFPSDividerGet)
	EncoderFPSDivider.AddCommand(EncoderFPSDividerSet)

	Root.PersistentFlags().Var(&LoggerLevel, "log-level", "")
	Root.PersistentFlags().String("remote-addr", "localhost:3594", "the address to an ffstream instance")
	Root.PersistentFlags().String("go-net-pprof-addr", "", "address to listen to for net/pprof requests")

	StatsEncoder.PersistentFlags().String("title", "", "stream title")
	StatsEncoder.PersistentFlags().String("description", "", "stream description")
	StatsEncoder.PersistentFlags().String("profile", "", "profile")

	Root.AddCommand(Buffer)
	Buffer.AddCommand(BufferOutput)
	BufferOutput.AddCommand(BufferOutputTolerable)
	BufferOutputTolerable.AddCommand(BufferOutputTolerableGet)
	BufferOutputTolerable.AddCommand(BufferOutputTolerableSet)

	Root.AddCommand(Pipelines)
	Pipelines.AddCommand(PipelinesGet)

	Root.AddCommand(AutoBitRate)
	AutoBitRate.AddCommand(AutoBitRateCalculator)
	AutoBitRateCalculator.AddCommand(AutoBitRateCalculatorGet)
	AutoBitRateCalculator.AddCommand(AutoBitRateCalculatorSet)
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

func encoderConfigGet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	cfg, err := client.GetRecoderConfig(ctx)
	assertNoError(ctx, err)

	jsonOutput(ctx, cmd.OutOrStdout(), cfg)
}

func encoderConfigSet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	cfg := jsonInput[streammuxtypes.RecoderConfig](ctx, cmd.InOrStdin())

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)
	client := client.New(remoteAddr)

	err = client.SetRecoderConfig(ctx, cfg)
	assertNoError(ctx, err)
}

// encoderFPSDividerGet calls the server and prints "num den\n"
func encoderFPSDividerGet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	c := client.New(remoteAddr)

	// expecting client.GetFPSDivider(ctx) to return (num uint32, den uint32, err error)
	num, den, err := c.GetFPSDivider(ctx)
	assertNoError(ctx, err)

	fmt.Fprintf(cmd.OutOrStdout(), "%d %d\n", num, den)
}

// encoderFPSDividerSet parses two integers (num den) and sends them to the server
func encoderFPSDividerSet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	num64, err := strconv.ParseUint(args[0], 10, 32)
	assertNoError(ctx, err)
	den64, err := strconv.ParseUint(args[1], 10, 32)
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	c := client.New(remoteAddr)

	// expecting client.SetFPSDivider(ctx, num uint32, den uint32) error
	err = c.SetFPSDivider(ctx, uint32(num64), uint32(den64))
	assertNoError(ctx, err)
}

func bufferOutputTolerableGet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	value, err := client.GetTolerableOutputQueueSizeBytes(ctx)
	assertNoError(ctx, err)

	fmt.Fprintf(cmd.OutOrStdout(), "%d\n", value)
}

func bufferOutputTolerableSet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	value, err := strconv.ParseInt(args[0], 10, 64)
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	err = client.SetTolerableOutputQueueSizeBytes(ctx, uint(value))
	assertNoError(ctx, err)
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

func autoBitRateCalculatorGet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	calculator, err := client.GetAutoBitRateCalculator(ctx)
	assertNoError(ctx, err)

	jsonOutput(ctx, cmd.OutOrStdout(), calculator)
}

func autoBitRateCalculatorSet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	// accept arbitrary JSON for the calculator configuration
	cfg := jsonInput[streammuxtypes.AutoBitrateCalculatorThresholds](ctx, cmd.InOrStdin())

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	err = client.SetAutoBitRateCalculator(ctx, &cfg)
	assertNoError(ctx, err)
}
