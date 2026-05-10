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

	// QuietOnOpenFailure demotes by-design steady-state log spam to
	// Debug when high-priority input slots are empty (the steady state
	// before a gRPC `inputs add` provisions them) or when a configured
	// upstream is not yet publishing. Three classes are gated:
	//   - input chain "input N error: <NewInput failure>"
	//   - autobitrate handler "unable to get encoder"
	//   - input-with-fallback "onInputChainError: unable to switch to fallback N: another switch is in progress (...)"
	// Default (false) preserves the legacy ERRO/WARN levels so existing
	// diagnostics aren't lost; set true (CLI: -quiet_on_open_failure,
	// legacy alias -quiet_empty_priority) to suppress the noise during
	// normal startup.
	QuietOnOpenFailure bool

	// ExitOnLastInputRemoved cancels the ffstream runtime after RemoveInput
	// removes the last registered input resource. This is for process-per-
	// source launchers whose owner maps "all inputs removed" to process
	// deactivation.
	ExitOnLastInputRemoved bool

	// DefaultRetryOutputTimeoutOnFailure is the retry-on-failure budget
	// applied to runtime-created output templates that do not carry an
	// explicit value. SetOutputURL's IDLE-START path (case 0) lazy-creates
	// a SenderTemplate purely from the URL — without this default, the
	// runtime template ends up with RetryOutputTimeoutOnFailure=0, which
	// makes senderFactory.NewSender take the no-retry newOutput() path
	// and produces a single fatal SwitchOutputByProps error on a transient
	// open failure. SetOutputURL's case 1 also applies this default when
	// the existing template's retry value is zero (e.g. AddOutputTemplate
	// was called without one); explicit non-zero values are preserved.
	// The cmd/ffstream launcher wires flags.RetryOutputTimeoutOnFailure
	// into this field so the gRPC-driven IDLE-START flow inherits the
	// boot-time retry budget.
	DefaultRetryOutputTimeoutOnFailure time.Duration

	// TCPMSS, when non-zero, is the FFmpeg `tcp_mss` AVOption value (in
	// bytes) injected by senderFactory.newOutputKernel into the
	// per-output CustomOptions dictionary for outbound TCP-class outputs
	// (URL schemes tcp/rtmp/rtmps), unless the per-output template
	// already carries an explicit tcp_mss (which wins). Default zero
	// means no injection: the FFmpeg tcp protocol falls back to the
	// system MSS negotiation.
	//
	// User constraint (verbatim, 2026-05-08):
	//   "tcp_mss overrides may be optional, but should not be
	//    hardcoded/mandated"
	// Hence the zero default is a deliberate operator-opt-in design,
	// not a placeholder for a compile-time recommended value.
	//
	// The injected option flows through libavformat/tcp.c customize_fd
	// into setsockopt(IPPROTO_TCP, TCP_MAXSEG) BEFORE connect, so the
	// SYN advertises the capped MSS. Useful for bypassing forward-path
	// drops caused by intermediate links with reduced effective MSS;
	// the cmd/ffstream launcher exposes this as the -tcp_mss flag.
	TCPMSS int
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

// OptionQuietOnOpenFailure sets Config.QuietOnOpenFailure. See the
// field doc.
type OptionQuietOnOpenFailure bool

func (o OptionQuietOnOpenFailure) apply(cfg *Config) {
	cfg.QuietOnOpenFailure = bool(o)
}

// OptionExitOnLastInputRemoved sets Config.ExitOnLastInputRemoved. See the
// field doc.
type OptionExitOnLastInputRemoved bool

func (o OptionExitOnLastInputRemoved) apply(cfg *Config) {
	cfg.ExitOnLastInputRemoved = bool(o)
}

// OptionDefaultRetryOutputTimeoutOnFailure sets
// Config.DefaultRetryOutputTimeoutOnFailure. See the field doc.
type OptionDefaultRetryOutputTimeoutOnFailure time.Duration

func (o OptionDefaultRetryOutputTimeoutOnFailure) apply(cfg *Config) {
	cfg.DefaultRetryOutputTimeoutOnFailure = time.Duration(o)
}

// OptionTCPMSS sets Config.TCPMSS. See the field doc.
type OptionTCPMSS int

func (o OptionTCPMSS) apply(cfg *Config) {
	cfg.TCPMSS = int(o)
}

// OptionQuietEmptyPriority is a deprecated alias of
// OptionQuietOnOpenFailure retained for backward compatibility with
// callers built against the pre-rename API.
//
// Deprecated: use OptionQuietOnOpenFailure.
type OptionQuietEmptyPriority = OptionQuietOnOpenFailure
