// Package ffflag provides a custom command-line flag parser designed for ffmpeg-like arguments.
//
// flag.go defines the core flag types and their parsing logic.
package ffflag

import (
	"strconv"
	"time"

	"github.com/dustin/go-humanize"
	loggertypes "github.com/facebookincubator/go-belt/tool/logger"
)

type OptionSettings struct {
	Name                  string
	CollectUnknownOptions bool
	WithArgument          bool
}

type GenericOption struct {
	OptionSettings
	Wrapped[any]
	CollectedUnknownOptions [][]string
	// changed is set true the first time the parser writes to this
	// option. Distinguishes "operator passed the flag" from "left at
	// registered default", which equality-against-default cannot do
	// (e.g. operator passing the same value as the default would
	// otherwise be indistinguishable). Callers consume it via
	// Option.Changed().
	changed bool
}

// Changed reports whether the parser observed this flag in argv. Use
// this in preference to comparing Value() against a known default —
// equality cannot tell "operator passed the default value verbatim"
// from "operator did not pass the flag at all".
func (opt Option[V, W]) Changed() bool {
	return opt.GenericOption.changed
}

type Option[V any, W Wrapped[V]] struct {
	*GenericOption
}

func (opt Option[V, W]) Value() V {
	if w, ok := opt.GenericOption.Wrapped.(abstractWrapper[V]); ok {
		return w.Wrapped.Value()
	}
	return opt.GenericOption.Wrapped.(Valuer[V]).Value()
}

type Wrapped[V any] interface {
	Valuer[V]
	Parse(input string) error
}

type Valuer[V any] interface {
	Value() V
}

type abstractWrapper[V any] struct {
	Wrapped[V]
}

func (w abstractWrapper[V]) Value() any {
	return w.Wrapped.Value()
}

type Uint64 uint64

func (v *Uint64) Parse(input string) error {
	i, err := humanize.ParseBytes(input)
	if err != nil {
		return err
	}
	*v = Uint64(i)
	return nil
}

func (v *Uint64) Value() uint64 {
	return uint64(*v)
}

type Float64 float64

func (v *Float64) Parse(input string) error {
	i, err := strconv.ParseFloat(input, 64)
	if err != nil {
		return err
	}
	*v = Float64(i)
	return nil
}

func (v *Float64) Value() float64 {
	return float64(*v)
}

type String string

func (v *String) Parse(input string) error {
	*v = String(input)
	return nil
}

func (v *String) Value() string {
	return string(*v)
}

type StringsAsSeparateFlags []string

func (v *StringsAsSeparateFlags) Parse(input string) error {
	*v = append(*v, input)
	return nil
}

func (v *StringsAsSeparateFlags) Value() []string {
	return []string(*v)
}

type Bool bool

func (v *Bool) Parse(input string) error {
	if input == "" {
		*v = true
		return nil
	}
	b, err := strconv.ParseBool(input)
	if err != nil {
		return err
	}
	*v = Bool(b)
	return nil
}

func (v *Bool) Value() bool {
	return bool(*v)
}

type LogLevel loggertypes.Level

func (v *LogLevel) Parse(input string) error {
	if input == "verbose" {
		*v = LogLevel(loggertypes.LevelDebug)
		return nil
	}
	return (*loggertypes.Level)(v).Set(input)
}

func (v *LogLevel) Value() loggertypes.Level {
	return loggertypes.Level(*v)
}

type Duration time.Duration

func (v *Duration) Parse(input string) error {
	d, err := time.ParseDuration(input)
	if err != nil {
		return err
	}
	*v = Duration(d)
	return nil
}

func (v *Duration) Value() time.Duration {
	return time.Duration(*v)
}
