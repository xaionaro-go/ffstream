//go:build with_libsrt
// +build with_libsrt

package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/client"
)

var (
	SRTFlagIntGet = &cobra.Command{
		Use:  "get",
		Args: cobra.ExactArgs(1),
		Run:  srtFlagIntGet,
	}

	SRTFlagIntSet = &cobra.Command{
		Use:  "set",
		Args: cobra.ExactArgs(2),
		Run:  srtFlagIntSet,
	}

	StatsOutputSRT = &cobra.Command{
		Use:  "output_srt",
		Args: cobra.ExactArgs(0),
		Run:  statsOutputSRT,
	}
)

func init() {
	StatsOutputSRT.PersistentFlags().Bool("json", false, "use JSON output format")

	Stats.AddCommand(StatsOutputSRT)

	Root.AddCommand(SRT)
	SRT.AddCommand(SRTFlag)
	SRTFlag.AddCommand(SRTFlagInt)
	SRTFlagInt.AddCommand(SRTFlagIntGet)
	SRTFlagInt.AddCommand(SRTFlagIntSet)
}

func statsOutputSRT(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	stats, err := client.GetOutputSRTStats(ctx)
	assertNoError(ctx, err)

	jsonOutput(ctx, cmd.OutOrStdout(), stats)
}

func srtFlagIntGet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	flagID, err := srtFlagNameToID(args[0])
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	value, err := client.GetSRTFlagInt(ctx, flagID)
	assertNoError(ctx, err)

	fmt.Fprintf(cmd.OutOrStdout(), "%d\n", value)
}

func srtFlagIntSet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	flagID, err := srtFlagNameToID(args[0])
	assertNoError(ctx, err)

	value, err := strconv.ParseInt(args[1], 10, 64)
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	err = client.SetSRTFlagInt(ctx, flagID, value)
	assertNoError(ctx, err)
}
