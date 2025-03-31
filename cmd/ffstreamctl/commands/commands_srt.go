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
	FlagIntGet = &cobra.Command{
		Use:  "get",
		Args: cobra.ExactArgs(1),
		Run:  flagIntGet,
	}

	FlagIntSet = &cobra.Command{
		Use:  "set",
		Args: cobra.ExactArgs(2),
		Run:  flagIntSet,
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

	Root.AddCommand(Flag)
	Flag.AddCommand(FlagInt)
	FlagInt.AddCommand(FlagIntGet)
	FlagInt.AddCommand(FlagIntSet)
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

func flagIntGet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	flagID, err := srtFlagNameToID(args[0])
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	value, err := client.GetFlagInt(ctx, flagID)
	assertNoError(ctx, err)

	fmt.Fprintf(cmd.OutOrStdout(), "%d\n", value)
}

func flagIntSet(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()

	flagID, err := srtFlagNameToID(args[0])
	assertNoError(ctx, err)

	value, err := strconv.ParseInt(args[1], 10, 64)
	assertNoError(ctx, err)

	remoteAddr, err := cmd.Flags().GetString("remote-addr")
	assertNoError(ctx, err)

	client := client.New(remoteAddr)

	err = client.SetFlagInt(ctx, flagID, value)
	assertNoError(ctx, err)
}
