// option.go provides functional options for configuring FFStream.

package ffstream

import "time"

// Config is a configuration of FFStream.
// Keep fields additive (backwards compatible).
type Config struct {
	// InputRetryInterval is a delay between input reconnect attempts.
	// Zero means: use the internal/default retry interval.
	InputRetryInterval time.Duration

	// FrameDropVideo, when true, makes the avpipeline serve loop drop
	// video frames whose downstream queue is full instead of blocking
	// the producer. This prevents the upstream wedge under transient
	// downstream backpressure (e.g. encoder/sender slowdowns) at the
	// cost of visible glitches. Default: true (most aggressive; the
	// wedge-protection knob).
	FrameDropVideo bool
	// FrameDropAudio mirrors FrameDropVideo for audio frames. Audio
	// drops are perceptible (clicks/gaps), so the conservative default
	// is false.
	FrameDropAudio bool
	// FrameDropOther mirrors FrameDropVideo for non-audio/video media
	// types (subtitles, data). Default: false.
	FrameDropOther bool

	// BridgePTSAcrossChains, when true, sets
	// barrierstategetter.SwitchFlagBridgePTSAcrossChains on the
	// InputWithFallback InputSwitch so the per-chain PTS-offset bridge in
	// avpipeline rebases the new chain's PTS/DTS at every chain switch,
	// keeping the output stream a strictly-monotonic continuation of the
	// previous chain's last PTS.
	//
	// This bridges cross-clock-domain transitions (e.g. rtmp upstream-
	// derived PTS vs builtin camera+mic monotonic-epoch PTS) that would
	// otherwise produce large forward / backward jumps at chain switches
	// (~600s observed on camera->rtmp) and freeze players. The flag in
	// avpipeline is OFF by default; the prod use-case here specifically
	// is the cross-clock-domain switch, so ffstream defaults this ON.
	BridgePTSAcrossChains bool

	// QuietEmptyPriority demotes by-design steady-state log spam to
	// Debug when high-priority input slots are empty (the steady state
	// before a gRPC `inputs add` provisions them). Three classes are
	// gated:
	//   - input chain "input N error: no input resources configured for priority N"
	//   - autobitrate handler "unable to get encoder"
	//   - input-with-fallback "onInputChainError: unable to switch to fallback N: another switch is in progress (...)"
	// Default (false) preserves the legacy ERRO/WARN levels so existing
	// diagnostics aren't lost; set true (CLI: -quiet_empty_priority) to
	// suppress the noise during normal startup.
	QuietEmptyPriority bool
}

func DefaultConfig() Config {
	return Config{
		InputRetryInterval:    -1,
		FrameDropVideo:        true,
		FrameDropAudio:        false,
		FrameDropOther:        false,
		BridgePTSAcrossChains: true,
	}
}

type Option interface {
	apply(*Config)
}

// Options is a helper wrapper around []Option.
type Options []Option

func (opts Options) apply(cfg *Config) {
	for _, opt := range opts {
		opt.apply(cfg)
	}
}

func (opts Options) Config() Config {
	cfg := DefaultConfig()
	opts.apply(&cfg)
	return cfg
}

type OptionInputRetryIntervalValue time.Duration

func (o OptionInputRetryIntervalValue) apply(cfg *Config) {
	cfg.InputRetryInterval = time.Duration(o)
}

func OptionInputRetryInterval(interval time.Duration) OptionInputRetryIntervalValue {
	return OptionInputRetryIntervalValue(interval)
}

// OptionFrameDropVideo sets Config.FrameDropVideo. See the field doc.
type OptionFrameDropVideo bool

func (o OptionFrameDropVideo) apply(cfg *Config) {
	cfg.FrameDropVideo = bool(o)
}

// OptionFrameDropAudio sets Config.FrameDropAudio. See the field doc.
type OptionFrameDropAudio bool

func (o OptionFrameDropAudio) apply(cfg *Config) {
	cfg.FrameDropAudio = bool(o)
}

// OptionFrameDropOther sets Config.FrameDropOther. See the field doc.
type OptionFrameDropOther bool

func (o OptionFrameDropOther) apply(cfg *Config) {
	cfg.FrameDropOther = bool(o)
}

// OptionBridgePTSAcrossChains sets Config.BridgePTSAcrossChains. See the
// field doc.
type OptionBridgePTSAcrossChains bool

func (o OptionBridgePTSAcrossChains) apply(cfg *Config) {
	cfg.BridgePTSAcrossChains = bool(o)
}

// OptionQuietEmptyPriority sets Config.QuietEmptyPriority. See the field
// doc.
type OptionQuietEmptyPriority bool

func (o OptionQuietEmptyPriority) apply(cfg *Config) {
	cfg.QuietEmptyPriority = bool(o)
}
