// Package buildinfo exposes build-time identity for ffstream binaries:
// the linker-injected buildvars (Version/GitCommit/BuildDate) and the
// Go runtime debug.BuildInfo (module path, vcs.* settings). Both
// ffstream and ffstreamctl render --version output through this
// package so the format stays identical across binaries.
package buildinfo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime/debug"

	"github.com/xaionaro-go/buildvars"
)

// BuildVars mirrors the linker-injected globals from
// github.com/xaionaro-go/buildvars. Empty strings are omitted from JSON
// because unstamped dev builds leave them blank.
type BuildVars struct {
	Version   string `json:",omitempty"`
	GitCommit string `json:",omitempty"`
	BuildDate string `json:",omitempty"`
}

// BuildInfo is the public --version payload. BuildVars carries the
// linker-stamped identity, BuildInfo carries the Go runtime view of the
// module graph and vcs.* settings observed by the toolchain.
type BuildInfo struct {
	BuildInfo *debug.BuildInfo `json:",omitempty"`
	BuildVars *BuildVars       `json:",omitempty"`
}

// Get assembles the current binary's BuildInfo. BuildVars is nil when
// every linker-stamped field is empty (unstamped dev build) so JSON
// output stays compact in that common case. BuildInfo is nil only when
// debug.ReadBuildInfo fails, which on a regular Go-built binary should
// not happen.
func Get() BuildInfo {
	result := BuildInfo{
		BuildVars: &BuildVars{
			Version:   buildvars.Version,
			GitCommit: buildvars.GitCommit,
			BuildDate: buildvars.BuildDateString,
		},
	}
	if *result.BuildVars == (BuildVars{}) {
		result.BuildVars = nil
	}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return result
	}
	result.BuildInfo = bi
	return result
}

// FindBuildInfoSetting returns the value of the named runtime build
// setting (e.g. "vcs.revision", "vcs.time", "vcs.modified") or "" when
// the setting is absent or BuildInfo could not be read.
func (b BuildInfo) FindBuildInfoSetting(key string) string {
	if b.BuildInfo == nil {
		return ""
	}
	for _, s := range b.BuildInfo.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

// JSON returns the indented JSON rendering used by the --version flag.
// The trailing newline matches encoding/json's Encoder.Encode behaviour
// so callers can write the buffer straight to stdout.
func JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", " ")
	if err := enc.Encode(Get()); err != nil {
		return nil, fmt.Errorf("unable to JSON-encode the build info: %w", err)
	}
	return buf.Bytes(), nil
}

// VersionString returns a single-line human-readable identity built
// from the linker-stamped buildvars and the runtime vcs.revision /
// vcs.time fallback. Used as cobra.Command.Version on ffstreamctl so
// "--version" prints something readable even on unstamped dev builds.
func VersionString() string {
	bi := Get()
	version := ""
	commit := ""
	date := ""
	if bi.BuildVars != nil {
		version = bi.BuildVars.Version
		commit = bi.BuildVars.GitCommit
		date = bi.BuildVars.BuildDate
	}
	if version == "" && bi.BuildInfo != nil {
		version = bi.BuildInfo.Main.Version
	}
	if commit == "" {
		commit = bi.FindBuildInfoSetting("vcs.revision")
	}
	if date == "" {
		date = bi.FindBuildInfoSetting("vcs.time")
	}
	modified := bi.FindBuildInfoSetting("vcs.modified")

	if version == "" {
		version = "(unknown)"
	}

	out := version
	if commit != "" {
		out += fmt.Sprintf(" (commit %s", commit)
		if modified == "true" {
			out += "-dirty"
		}
		if date != "" {
			out += ", " + date
		}
		out += ")"
	} else if date != "" {
		out += " (" + date + ")"
	}
	return out
}
