package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strconv"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/spf13/cobra"
	"github.com/xaionaro-go/avpipeline/indicator"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/client"
	"github.com/xaionaro-go/observability"
	"github.com/xaionaro-go/polyjson"
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

	Encoder = &cobra.Command{
		Use: "encoder",
	}

	EncoderConfig = &cobra.Command{
		Use: "config",
	}

	EncoderAutoBitRate = &cobra.Command{
		Use: "auto_bitrate",
	}

	EncoderAutoBitRateCalculator = &cobra.Command{
		Use: "calculator",
	}

	EncoderAutoBitRateCalculatorGet = &cobra.Command{
		Use:  "get",
		Args: cobra.ExactArgs(0),
		Run:  autoBitRateCalculatorGet,
	}

	EncoderAutoBitRateCalculatorSet = &cobra.Command{
		Use:  "set",
		Args: cobra.ExactArgs(0),
		Run:  autoBitRateCalculatorSet,
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
)

func init() {
	Root.AddCommand(Stats)
	Stats.AddCommand(StatsEncoder)

	Root.AddCommand(Encoder)
	Encoder.AddCommand(EncoderConfig)

	Encoder.AddCommand(EncoderAutoBitRate)
	EncoderAutoBitRate.AddCommand(EncoderAutoBitRateCalculator)
	EncoderAutoBitRateCalculator.AddCommand(EncoderAutoBitRateCalculatorGet)
	EncoderAutoBitRateCalculator.AddCommand(EncoderAutoBitRateCalculatorSet)

	Encoder.AddCommand(EncoderFPSFraction)
	EncoderFPSFraction.AddCommand(EncoderFPSFractionGet)
	EncoderFPSFraction.AddCommand(EncoderFPSFractionSet)

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

	polyjson.AutoRegisterTypes = true
	polyjson.RegisterType(streammuxtypes.AutoBitrateCalculatorThresholds{})
	polyjson.RegisterType(streammuxtypes.AutoBitrateCalculatorLogK{})
	polyjson.RegisterType(streammuxtypes.AutoBitrateCalculatorStatic(0))
	polyjson.RegisterType(streammuxtypes.AutoBitrateCalculatorQueueSizeGapDecay{})
	polyjson.RegisterType(indicator.MAMA[float64]{})
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
	err = client.SetAutoBitRateCalculator(ctx, m["calculator"])
	assertNoError(ctx, err)
}
