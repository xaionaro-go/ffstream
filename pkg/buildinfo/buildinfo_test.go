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

// JSON output must round-trip through encoding/json into the curated
// versionJSONPayload shape. The payload deliberately omits
// debug.BuildInfo.Main.Path and Deps (those differ between cmd/ffstream
// and cmd/ffstreamctl); only BuildVars and the allowlisted vcs.*
// settings are rendered.
func TestJSON_RoundTrip(t *testing.T) {
	b, err := JSON()
	require.NoError(t, err)
	require.NotEmpty(t, b)
	assert.True(t, strings.HasSuffix(string(b), "\n"), "Encoder.Encode must keep trailing newline")

	var decoded versionJSONPayload
	require.NoError(t, json.Unmarshal(b, &decoded))
	// On a Go-built test binary the toolchain always populates GOOS;
	// at least one allowlisted setting must therefore appear.
	require.NotNil(t, decoded.VCSSettings)
	assert.NotEmpty(t, decoded.VCSSettings["GOOS"], "GOOS must be present in the curated payload")
}

// TestJSON_OmitsBinarySpecificFields pins the byte-for-byte cross-
// binary identity contract: --version output MUST NOT include the
// fields that necessarily differ between cmd/ffstream and
// cmd/ffstreamctl, namely the module Main.Path and the Deps[] graph.
// Including them would silently break the byte-for-byte equality
// claimed in changes_since_sunday.md / cmd/ffstreamctl/commands.go.
func TestJSON_OmitsBinarySpecificFields(t *testing.T) {
	b, err := JSON()
	require.NoError(t, err)
	s := string(b)
	assert.NotContains(t, s, `"Main"`, "JSON must not embed debug.BuildInfo.Main (Path differs per binary)")
	assert.NotContains(t, s, `"Path"`, "JSON must not embed Main.Path (cmd/ffstream vs cmd/ffstreamctl)")
	assert.NotContains(t, s, `"Deps"`, "JSON must not embed Deps[] (per-binary subgraph)")
}

// TestVersionPayload_StableAcrossSyntheticDiffs simulates two binaries
// built from the same tree by swapping debug.BuildInfo.Main.Path and
// the Deps slice on a synthetic BuildInfo. The curated payload must be
// identical for both — that is the byte-for-byte contract.
func TestVersionPayload_StableAcrossSyntheticDiffs(t *testing.T) {
	base := Get()
	require.NotNil(t, base.BuildInfo)

	a := base
	aBI := *base.BuildInfo
	aBI.Main.Path = "github.com/xaionaro-go/ffstream/cmd/ffstream"
	aBI.Deps = nil
	a.BuildInfo = &aBI

	b := base
	bBI := *base.BuildInfo
	bBI.Main.Path = "github.com/xaionaro-go/ffstream/cmd/ffstreamctl"
	bBI.Deps = nil
	b.BuildInfo = &bBI

	pa := versionPayload(a)
	pb := versionPayload(b)
	assert.Equal(t, pa, pb, "curated payload must ignore Main.Path / Deps so cross-binary --version is byte-identical")
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
