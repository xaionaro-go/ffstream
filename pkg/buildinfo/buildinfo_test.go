package buildinfo

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Get must always return a usable BuildInfo when run under `go test`:
// the test binary itself is a Go-built module, so debug.ReadBuildInfo
// succeeds and Main.Path is populated.
func TestGet_ReturnsRuntimeBuildInfo(t *testing.T) {
	bi := Get()
	require.NotNil(t, bi.BuildInfo, "debug.ReadBuildInfo must succeed under go test")
	assert.NotEmpty(t, bi.BuildInfo.Main.Path, "Main.Path must be set for the running test binary")
}

// JSON output must round-trip through encoding/json into the same
// BuildInfo shape used by Get(). Missing fields means a JSON tag
// regression.
func TestJSON_RoundTrip(t *testing.T) {
	b, err := JSON()
	require.NoError(t, err)
	require.NotEmpty(t, b)
	assert.True(t, strings.HasSuffix(string(b), "\n"), "Encoder.Encode must keep trailing newline")

	var decoded BuildInfo
	require.NoError(t, json.Unmarshal(b, &decoded))
	require.NotNil(t, decoded.BuildInfo)
	assert.Equal(t, Get().BuildInfo.Main.Path, decoded.BuildInfo.Main.Path)
}

// VersionString must be non-empty even on an unstamped build (no
// linker -X flags) — the runtime vcs.* fallback covers it. The
// "(unknown)" sentinel guards against an all-empty result.
func TestVersionString_NonEmpty(t *testing.T) {
	v := VersionString()
	assert.NotEmpty(t, v)
}

// FindBuildInfoSetting returns "" for unknown keys and a non-empty
// value for at least one well-known runtime key on a Go-built test
// binary. We assert on GOOS because vcs.* settings are absent when the
// test binary is built outside a VCS checkout.
func TestFindBuildInfoSetting(t *testing.T) {
	bi := Get()
	assert.Equal(t, "", bi.FindBuildInfoSetting("definitely-not-a-real-setting"))
	assert.NotEmpty(t, bi.FindBuildInfoSetting("GOOS"), "GOOS is always populated by the Go toolchain")
}
