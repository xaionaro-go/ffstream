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
	transcodertypes "github.com/xaionaro-go/avpipeline/preset/transcoderwithpassthrough/types"
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
)

func init() {
	Root.AddCommand(Stats)
	Stats.AddCommand(StatsEncoder)

	Root.AddCommand(EncoderConfig)
	EncoderConfig.AddCommand(EncoderConfigGet)
	EncoderConfig.AddCommand(EncoderConfigSet)

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

	cfg := jsonInput[transcodertypes.RecoderConfig](ctx, cmd.InOrStdin())

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)
	client := client.New(remoteAddr)

	err = client.SetRecoderConfig(ctx, cfg)
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
