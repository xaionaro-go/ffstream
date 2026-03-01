package ffmonitor

import (
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFlags_Defaults(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	AddFlags(cmd)

	cfg, err := ParseFlags(cmd)
	require.NoError(t, err)

	assert.False(t, cfg.IncludePacketPayload)
	assert.False(t, cfg.IncludeFramePayload)
	assert.False(t, cfg.DoDecode)
	assert.Equal(t, "plaintext", cfg.Format)
	assert.Equal(t, "", cfg.InputFormat)
	assert.Equal(t, time.Duration(0), cfg.HighlightDiscontinuity)
	assert.Nil(t, cfg.StreamIndices)
}

func TestParseFlags_AllSet(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	AddFlags(cmd)

	require.NoError(t, cmd.Flags().Set("include-packet-payload", "true"))
	require.NoError(t, cmd.Flags().Set("include-frame-payload", "true"))
	require.NoError(t, cmd.Flags().Set("do-decode", "true"))
	require.NoError(t, cmd.Flags().Set("format", "json"))
	require.NoError(t, cmd.Flags().Set("input-format", "h264"))
	require.NoError(t, cmd.Flags().Set("highlight-discontinuity", "1s"))
	require.NoError(t, cmd.Flags().Set("stream-indices", "0,1,2"))

	cfg, err := ParseFlags(cmd)
	require.NoError(t, err)

	assert.True(t, cfg.IncludePacketPayload)
	assert.True(t, cfg.IncludeFramePayload)
	assert.True(t, cfg.DoDecode)
	assert.Equal(t, "json", cfg.Format)
	assert.Equal(t, "h264", cfg.InputFormat)
	assert.Equal(t, time.Second, cfg.HighlightDiscontinuity)
	assert.Equal(t, []int{0, 1, 2}, cfg.StreamIndices)
}
