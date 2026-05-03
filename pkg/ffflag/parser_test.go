package ffflag

import (
	"testing"
	"time"

	loggertypes "github.com/facebookincubator/go-belt/tool/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUint64_Parse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    uint64
		wantErr bool
	}{
		{"plain number", "1024", 1024, false},
		{"zero", "0", 0, false},
		{"kB suffix", "1KB", 1000, false},
		{"MB suffix", "512MB", 512000000, false},
		{"invalid string", "abc", 0, true},
		{"empty string", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Uint64(0)
			err := v.Parse(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, v.Value())
			}
		})
	}
}

func TestFloat64_Parse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    float64
		wantErr bool
	}{
		{"positive", "3.14", 3.14, false},
		{"negative", "-1.5", -1.5, false},
		{"zero", "0", 0.0, false},
		{"scientific", "1e10", 1e10, false},
		{"invalid", "not-a-number", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Float64(0)
			err := v.Parse(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.InDelta(t, tt.want, v.Value(), 0.001)
			}
		})
	}
}

func TestString_Parse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"normal string", "hello", "hello"},
		{"empty string", "", ""},
		{"special chars", "rtmp://server:1935/live", "rtmp://server:1935/live"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := String("")
			err := v.Parse(tt.input)
			require.NoError(t, err, "String.Parse should never error")
			assert.Equal(t, tt.want, v.Value())
		})
	}
}

func TestStringsAsSeparateFlags_Parse(t *testing.T) {
	v := StringsAsSeparateFlags(nil)
	require.NoError(t, v.Parse("input1.flv"))
	require.NoError(t, v.Parse("input2.flv"))
	require.NoError(t, v.Parse("input3.flv"))
	assert.Equal(t, []string{"input1.flv", "input2.flv", "input3.flv"}, v.Value())
}

func TestBool_Parse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    bool
		wantErr bool
	}{
		{"empty string means true", "", true, false},
		{"true", "true", true, false},
		{"false", "false", false, false},
		{"1", "1", true, false},
		{"0", "0", false, false},
		{"invalid", "invalid", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Bool(false)
			err := v.Parse(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, v.Value())
			}
		})
	}
}

func TestLogLevel_Parse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    loggertypes.Level
		wantErr bool
	}{
		{"verbose maps to debug", "verbose", loggertypes.LevelDebug, false},
		{"warning", "warning", loggertypes.LevelWarning, false},
		{"error", "error", loggertypes.LevelError, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := LogLevel(loggertypes.LevelInfo)
			err := v.Parse(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, v.Value())
			}
		})
	}
}

func TestDuration_Parse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"seconds", "5s", 5 * time.Second, false},
		{"milliseconds", "100ms", 100 * time.Millisecond, false},
		{"compound", "1h30m", 90 * time.Minute, false},
		{"invalid", "invalid", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Duration(0)
			err := v.Parse(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, v.Value())
			}
		})
	}
}

func TestParser_Parse_BasicFlow(t *testing.T) {
	p := NewParser()
	inputsFlag := AddParameter(p, "i", true, ptr(StringsAsSeparateFlags(nil)))
	verboseFlag := AddFlag(p, "v", false)

	err := p.Parse([]string{"-v", "-i", "rtsp://foo", "-i", "rtmp://bar"})
	require.NoError(t, err)

	assert.True(t, verboseFlag.Value())
	assert.Equal(t, []string{"rtsp://foo", "rtmp://bar"}, inputsFlag.Value())
}

func TestParser_Parse_UnknownFlagsCollected(t *testing.T) {
	p := NewParser()
	inputsFlag := AddParameter(p, "i", true, ptr(StringsAsSeparateFlags(nil)))

	err := p.Parse([]string{"-f", "mpegts", "-re", "-i", "input.flv", "-c:v", "libx264"})
	require.NoError(t, err)

	assert.Equal(t, []string{"input.flv"}, inputsFlag.Value())
	// Unknown flags before -i should be collected with -i
	require.Len(t, inputsFlag.CollectedUnknownOptions, 1)
	assert.Equal(t, []string{"-f", "mpegts", "-re"}, inputsFlag.CollectedUnknownOptions[0])
	// Unknown flags after the last known flag end up in parser's collector
	assert.Equal(t, []string{"-c:v", "libx264"}, p.CollectedUnknownOptions)
}

func TestParser_Parse_DoubleDashStopsProcessing(t *testing.T) {
	p := NewParser()
	inputsFlag := AddParameter(p, "i", true, ptr(StringsAsSeparateFlags(nil)))

	err := p.Parse([]string{"-i", "input.flv", "--", "-extra", "arg"})
	require.NoError(t, err)

	assert.Equal(t, []string{"input.flv"}, inputsFlag.Value())
	assert.Equal(t, []string{"-extra", "arg"}, p.CollectedNonFlags)
}

func TestParser_Parse_FlagWithoutRequiredArgument(t *testing.T) {
	p := NewParser()
	AddParameter(p, "i", true, ptr(StringsAsSeparateFlags(nil)))

	err := p.Parse([]string{"-i"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires an argument")
}

func TestParser_Parse_InvalidArgumentToFlag(t *testing.T) {
	p := NewParser()
	AddParameter(p, "b", false, ptr(Uint64(0)))

	err := p.Parse([]string{"-b", "not-a-number"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to parse")
}

func TestParser_Parse_DoubleDashFlagForm(t *testing.T) {
	// Both "-version" (ffmpeg-style) and "--version" (GNU-style) must
	// resolve to the same registered flag; the bare "--" terminator
	// must keep its original "stop processing" semantics.
	t.Run("double dash short form", func(t *testing.T) {
		p := NewParser()
		v := AddFlag(p, "version", false)
		require.NoError(t, p.Parse([]string{"--version"}))
		assert.True(t, v.Value())
	})
	t.Run("single dash short form still works", func(t *testing.T) {
		p := NewParser()
		v := AddFlag(p, "version", false)
		require.NoError(t, p.Parse([]string{"-version"}))
		assert.True(t, v.Value())
	})
	t.Run("bare double dash still terminates", func(t *testing.T) {
		p := NewParser()
		v := AddFlag(p, "version", false)
		require.NoError(t, p.Parse([]string{"--", "--version"}))
		assert.False(t, v.Value(), "tokens after -- must not be parsed as flags")
		assert.Equal(t, []string{"--version"}, p.CollectedNonFlags)
	})
}

// TestDoubleDashFlagForm_EqualsForm pins the GNU "--name=VALUE" form
// alongside the existing space-separated form. Pre-fix, the parser only
// supported "--name VALUE" (or "-name VALUE") and treated "--name=VALUE"
// as an unknown flag.
func TestDoubleDashFlagForm_EqualsForm(t *testing.T) {
	t.Run("equals form parameter", func(t *testing.T) {
		p := NewParser()
		v := AddParameter(p, "host", false, ptr(String("")))
		require.NoError(t, p.Parse([]string{"--host=example.com"}))
		assert.Equal(t, "example.com", v.Value())
		assert.True(t, v.Changed())
	})
	t.Run("equals form bool flag", func(t *testing.T) {
		p := NewParser()
		v := AddFlag(p, "debug", false)
		require.NoError(t, p.Parse([]string{"--debug=true"}))
		assert.True(t, v.Value())
		assert.True(t, v.Changed())
	})
	t.Run("equals form bool flag false", func(t *testing.T) {
		p := NewParser()
		v := AddFlag(p, "debug", false)
		require.NoError(t, p.Parse([]string{"--debug=false"}))
		assert.False(t, v.Value())
		assert.True(t, v.Changed(), "flag was passed; only its value was false")
	})
	t.Run("equals form short single-dash", func(t *testing.T) {
		p := NewParser()
		v := AddParameter(p, "host", false, ptr(String("")))
		require.NoError(t, p.Parse([]string{"-host=example.com"}))
		assert.Equal(t, "example.com", v.Value())
	})
	t.Run("equals empty value", func(t *testing.T) {
		p := NewParser()
		v := AddParameter(p, "host", false, ptr(String("default")))
		require.NoError(t, p.Parse([]string{"--host="}))
		assert.Equal(t, "", v.Value())
	})
}

// TestOption_Changed pins the operator-passed-vs-default discrimination
// contract. Direct equality against a registered default cannot tell
// "operator typed the default value verbatim" from "flag absent" — the
// Changed() bit is what callers use to refuse silent-no-op /
// conflict-fatal interactions.
func TestOption_Changed(t *testing.T) {
	t.Run("absentMeansFalse", func(t *testing.T) {
		p := NewParser()
		v := AddParameter(p, "x", false, ptr(Uint64(42)))
		require.NoError(t, p.Parse(nil))
		assert.False(t, v.Changed())
		assert.Equal(t, uint64(42), v.Value())
	})
	t.Run("presentMeansTrueEvenIfValueEqualsDefault", func(t *testing.T) {
		p := NewParser()
		v := AddParameter(p, "x", false, ptr(Uint64(42)))
		require.NoError(t, p.Parse([]string{"-x", "42"}))
		assert.True(t, v.Changed(), "operator passed the flag verbatim; equality-with-default would miss this")
		assert.Equal(t, uint64(42), v.Value())
	})
	t.Run("flagFormNoArgument", func(t *testing.T) {
		p := NewParser()
		v := AddFlag(p, "verbose", false)
		require.NoError(t, p.Parse([]string{"-verbose"}))
		assert.True(t, v.Changed())
		assert.True(t, v.Value())
	})
}

func TestParser_NewDefaultParser(t *testing.T) {
	p := NewDefaultParser()
	require.Len(t, p.Options, 1)
	assert.Equal(t, "i", p.Options[0].Name)

	err := p.Parse([]string{"-i", "input1", "-i", "input2"})
	require.NoError(t, err)
}
