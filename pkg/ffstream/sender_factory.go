// sender_factory.go implements SenderFactory to create output nodes based on URL templates and bitrate configurations.

package ffstream

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/codec"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/net/raw"
	"github.com/xaionaro-go/avpipeline/node"
	packetcondition "github.com/xaionaro-go/avpipeline/packet/condition"
	"github.com/xaionaro-go/avpipeline/packet/filter/removefiller"
	streammux "github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/avpipeline/processor"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/secret"
	tcpopt "github.com/xaionaro-go/tcp/opt"
)

// tcpMSSCappedSchemes lists the URL schemes whose underlying transport
// is plain TCP and therefore honours FFmpeg's `tcp_mss` AVOption via
// libavformat/tcp.c. Other transport classes (file, http*, srt over
// UDP, ...) are intentionally excluded: HTTP has its own MSS plumbing
// path, SRT is UDP-based, file outputs have no socket. Keys are
// lowercase because Go's url.Parse normalizes scheme to lowercase per
// RFC 3986 §3.1.
var tcpMSSCappedSchemes = map[string]struct{}{
	"tcp":   {},
	"rtmp":  {},
	"rtmps": {},
}

// tcpMSSPlausibleMin matches the Linux kernel's TCP_MIN_MSS hard floor
// (linux/include/net/tcp.h: `#define TCP_MIN_MSS 88U` with the kernel
// comment `Minimal accepted MSS. It is (60+60+8) - (20+20).` —
// max IPv4 + max TCP header sizes plus an 8-byte buffer minus the
// default IPv4+TCP header sizes). Operator values below this are
// silently clamped upward by the Linux kernel; the helper Warns so
// the operator sees the gap between configured and effective MSS.
//
// RFC 879's MSS-default of 536 (576-byte IPv4 minimum reassembly
// buffer minus 40-byte IPv4+TCP header floor) is a SEPARATE,
// HIGHER-VALUED concept and is intentionally not used here: 88 is
// the Linux-kernel-enforced floor for setsockopt acceptance, which
// is the gating semantic the Warn is communicating to the operator.
//
// Platform scope: 88 is a Linux-kernel-specific value. FFmpeg's
// libavformat/tcp.c setsockopt path is portable across non-Windows
// platforms (gated by `#if !HAVE_WINSOCK2_H`), but kernel-clamp
// semantics on non-Linux Unix (BSD/macOS) are implementation-
// specific and may differ. ffstream's primary deployment target is
// Android/Linux, so this heuristic is calibrated accordingly;
// operators on other platforms should verify per-kernel.
const tcpMSSPlausibleMin = 88

// tcpMSSPlausibleMax is a heuristic Ethernet baseline (1500-byte
// MTU); the kernel itself uses the actual egress interface MTU, not
// a fixed value, per tcp(7) "Values greater than the (eventual)
// interface MTU have no effect". The constant is intentionally
// coarse rather than a runtime egress-MTU lookup: the latter is
// over-engineering for an operator-visibility Warn whose typical
// audience is Ethernet/Wi-Fi 1500-byte MTU paths.
//
// Known false-positive: jumbo-frame interfaces (MTU 9000) accept
// `-tcp_mss 9000` without kernel clamping, so the Warn fires
// without a real configured-vs-effective gap. Known false-negative:
// PPPoE (MTU 1492) and tunnels (1380-1450) silently clamp values in
// (interface MTU, 1500] without the helper warning. Both are
// acceptable noise; operators on non-typical L2 envelopes are
// expected to verify with `ss -tinp` directly.
const tcpMSSPlausibleMax = 1500

// ensureTCPMSS injects a `tcp_mss=<tcpMSS>` AVOption into `opts` when
// `tcpMSS > 0`, the URL scheme is TCP-class (tcp/rtmp/rtmps), and the
// caller has not already supplied a `tcp_mss` value (user-set wins).
// `tcpMSS <= 0` means unset → return opts unchanged. The returned
// slice is fresh; the input slice is never mutated.
//
// The injected option flows through kernel.NewOutputFromURL into
// FFmpeg's libavformat/tcp.c `customize_fd`, which calls
// setsockopt(IPPROTO_TCP, TCP_MAXSEG) BEFORE `connect()`. That cap
// constrains the advertised MSS in the SYN — useful for bypassing
// forward-path drops on intermediate links with reduced effective
// MSS. The value is operator-driven via Config.TCPMSS (cmd/ffstream
// `-tcp_mss` flag); there is no compile-time default.
//
// The post-connect `WithRawNetworkConn` callback in newOutputKernel
// (used for ThinLinearTimeouts/ThinDupAck) is too late for this
// purpose: it fires after `OpenIOContext` returns successfully,
// which never happens when the handshake is dropped at a forward-
// path cliff.
//
// When tcpMSS falls outside [tcpMSSPlausibleMin, tcpMSSPlausibleMax]
// the helper emits a Warn so the operator sees that the kernel will
// silently clamp the value (per tcp(7) "TCP will also impose its
// minimum and maximum bounds over the value provided"). The option
// is still injected; clamping happens regardless and operators have
// reasons to push limits in lab settings.
func ensureTCPMSS(
	ctx context.Context,
	opts []avptypes.DictionaryItem,
	rawURL string,
	tcpMSS int,
) []avptypes.DictionaryItem {
	if tcpMSS <= 0 {
		return opts
	}
	if rawURL == "" {
		return opts
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed == nil {
		return opts
	}
	// url.Parse normalizes scheme to lowercase per RFC 3986 §3.1, so
	// no explicit ToLower is needed for the map lookup.
	if _, ok := tcpMSSCappedSchemes[parsed.Scheme]; !ok {
		return opts
	}
	for _, o := range opts {
		if o.Key == "tcp_mss" {
			return opts
		}
	}
	if tcpMSS < tcpMSSPlausibleMin || tcpMSS > tcpMSSPlausibleMax {
		logger.Warnf(ctx,
			"tcp_mss=%d is outside the plausible [%d, %d] range; the kernel will silently clamp it (tcp(7))",
			tcpMSS, tcpMSSPlausibleMin, tcpMSSPlausibleMax,
		)
	}
	out := make([]avptypes.DictionaryItem, len(opts), len(opts)+1)
	copy(out, opts)
	return append(out, avptypes.DictionaryItem{Key: "tcp_mss", Value: strconv.Itoa(tcpMSS)})
}

type SenderTemplate struct {
	URLTemplate                 string
	Options                     []avptypes.DictionaryItem
	RetryOutputTimeoutOnFailure time.Duration
}

func (t *SenderTemplate) GetURL(
	ctx context.Context,
	outputKey streammux.SenderKey,
) string {
	url := t.URLTemplate
	var audioSampleRate, videoWidth, videoHeight string
	if outputKey.AudioSampleRate != 0 {
		audioSampleRate = fmt.Sprintf("%d", outputKey.AudioSampleRate)
	}
	if outputKey.VideoResolution.Width != 0 {
		videoWidth = fmt.Sprintf("%d", outputKey.VideoResolution.Width)
	}
	if outputKey.VideoResolution.Height != 0 {
		videoHeight = fmt.Sprintf("%d", outputKey.VideoResolution.Height)
	}
	url = strings.ReplaceAll(url, "${a:0:codec}", string(codec.Name(outputKey.AudioCodec).Canonicalize(ctx, true)))
	url = strings.ReplaceAll(url, "${a:0:rate}", audioSampleRate)
	url = strings.ReplaceAll(url, "${v:0:codec}", string(codec.Name(outputKey.VideoCodec).Canonicalize(ctx, true)))
	url = strings.ReplaceAll(url, "${v:0:width}", videoWidth)
	url = strings.ReplaceAll(url, "${v:0:height}", videoHeight)
	return url
}

type senderFactory FFStream

var (
	_ streammux.SenderFactory[CustomData] = (*senderFactory)(nil)
	_ streammux.SenderURLPreviewer        = (*senderFactory)(nil)
)

func (s *FFStream) asSenderFactory() *senderFactory {
	return (*senderFactory)(s)
}

func (s *senderFactory) asFFStream() *FFStream {
	return (*FFStream)(s)
}

type SendingNodeAbstract interface {
	streammux.SendingNode[CustomData]
	streammux.SetDropOnCloser
}

func (s *senderFactory) NewSender(
	ctx context.Context,
	outputKey streammux.SenderKey,
) (streammux.SendingNode[CustomData], streammuxtypes.SenderConfig, error) {
	if len(s.OutputTemplates) != 1 {
		return nil, streammuxtypes.SenderConfig{}, fmt.Errorf("exactly one output template is required, got %d", len(s.OutputTemplates))
	}
	outputTemplate := s.OutputTemplates[0]
	outputURL := outputTemplate.GetURL(ctx, outputKey)
	var sendBufSize uint
	if ffstream := s.asFFStream(); ffstream != nil {
		if streamMux := ffstream.StreamMux; streamMux != nil {
			if autoBitrateHandler := streamMux.AutoBitRateHandler; autoBitrateHandler != nil {
				logger.Debugf(ctx, "NewSender: calculating send buffer size for output key %v using AutoBitRateVideoConfig", outputKey)
				resCfg := autoBitrateHandler.AutoBitRateVideoConfig.ResolutionsAndBitRates.Find(outputKey.VideoResolution)
				if resCfg == nil {
					if outputKey.VideoResolution != (codec.Resolution{}) {
						logger.Errorf(ctx, "unable to find bitrate config for resolution %v, using default send buffer size", outputKey.VideoResolution)
					}
					resCfg = autoBitrateHandler.AutoBitRateVideoConfig.ResolutionsAndBitRates.Best()
				}
				sendBufSize = uint(resCfg.BitrateHigh.ToBps()) // the buffer should be maxed out if we send traffic over 1000ms round-trip latency channel.
				sendBufSize = max(sendBufSize, 10*1024)        // at least 10KB
			}
		}
	}
	if outputTemplate.RetryOutputTimeoutOnFailure != 0 {
		return s.newOutputWithRetry(ctx, outputTemplate, outputURL, sendBufSize, outputTemplate.RetryOutputTimeoutOnFailure)
	}
	return s.newOutput(ctx, outputTemplate, outputURL, sendBufSize)
}

// URLForKey implements streammux.SenderURLPreviewer (Task #174). It
// returns the URL the factory would generate for outputKey via the
// next NewSender call, WITHOUT actually constructing a sender. The
// streammux Reuse path uses this to detect SetOutputURL drift between
// the existing reused output's URL and the URL the factory would now
// produce; on mismatch the existing output is torn down and the
// Create path picks up the new URL.
//
// Returns ("", nil) when no output template is registered (the IDLE
// pre-SetOutputURL state); the streammux Reuse path treats empty as
// "preview not currently available" and preserves regular Reuse
// semantics. Multi-template configurations error out for parity with
// NewSender.
func (s *senderFactory) URLForKey(
	ctx context.Context,
	outputKey streammux.SenderKey,
) (string, error) {
	switch len(s.OutputTemplates) {
	case 0:
		return "", nil
	case 1:
		return s.OutputTemplates[0].GetURL(ctx, outputKey), nil
	default:
		return "", fmt.Errorf("exactly one output template is required, got %d", len(s.OutputTemplates))
	}
}

func (s *senderFactory) newOutputKernel(
	ctx context.Context,
	outputTemplate SenderTemplate,
	outputURL string,
	bufSize uint,
) (_ret *kernel.Output, _err error) {
	logger.Debugf(ctx, "newOutputKernel(ctx, %#+v, %q, %d)", outputTemplate, outputURL, bufSize)
	defer func() {
		logger.Debugf(ctx, "/newOutputKernel(ctx, %#+v, %q, %d): %#+v, %v", outputTemplate, outputURL, bufSize, _ret, _err)
	}()
	waitForStreams := kernel.OutputConfigWaitForOutputStreams{}
	if s.StreamMux != nil {
		switch s.StreamMux.MuxMode {
		case streammuxtypes.UndefinedMuxMode:
			return nil, fmt.Errorf("undefined mux mode")
		case streammuxtypes.MuxModeForbid:
		case streammuxtypes.MuxModeSameOutputSameTracks:
		case streammuxtypes.MuxModeSameOutputDifferentTracks:
		case streammuxtypes.MuxModeDifferentOutputsSameTracks:
		case streammuxtypes.MuxModeDifferentOutputsSameTracksSplitAV:
			waitForStreams.VideoBeforeAudio = ptr(false)
		default:
			return nil, fmt.Errorf("unknown mux mode: %q", s.StreamMux.MuxMode)
		}
	}
	// Inject the operator-configured tcp_mss cap (Config.TCPMSS) for
	// TCP-class outputs so the FFmpeg avformat tcp protocol calls
	// setsockopt(TCP_MAXSEG) BEFORE connect. See ensureTCPMSS for the
	// rationale; the post-connect WithRawNetworkConn callback below is
	// too late for this purpose. Config.TCPMSS=0 (default) skips the
	// injection and lets FFmpeg fall back to system MSS negotiation.
	customOptions := ensureTCPMSS(ctx, outputTemplate.Options, outputURL, s.Config.TCPMSS)
	cfg := kernel.OutputConfig{
		CustomOptions:  customOptions,
		SendBufferSize: bufSize,
		// OpenTimeout bounds avformat_open_input via avpipeline's Go-side
		// ctx-watchdog + astiav.IOInterrupter (FFmpeg interrupt_callback
		// wrapper). 10s matches typical RTMP handshake budget with margin
		// for slow-start TLS-class scenarios; on timeout, kernel.Output
		// returns "Connection timed out" / openCtx.Err() instead of
		// blocking the caller behind a stalled cgo avio_open2 syscall.
		// See avpipeline kernel/output.go:562-606 for the watchdog
		// mechanism and avpipeline kernel/output_test.go for coverage of
		// this bound.
		OpenTimeout:                   10 * time.Second,
		WaitForOutputStreams:          &waitForStreams,
		IgnoreNoSourceFormatCtxErrors: true,
	}
	outputKernel, err := kernel.NewOutputFromURL(ctx, outputURL, secret.New(""), cfg)
	switch {
	case errors.As(err, &kernel.ErrUnableToSetSendBufferSize{}):
		logger.Warnf(ctx, "unable to set send buffer size, retrying with 0 send buffer size: %v", err)
		cfg.SendBufferSize = 0
		outputKernel, err = kernel.NewOutputFromURL(ctx, outputURL, secret.New(""), cfg)
	}
	if err != nil {
		return nil, fmt.Errorf("unable to create output from URL %q: %w", outputURL, err)
	}

	err = outputKernel.WithRawNetworkConn(ctx, func(ctx context.Context, rawConn syscall.RawConn, netName string) error {
		switch netName {
		case "tcp", "tcp4", "tcp6":
			return raw.SetTCPSockOptions(ctx, rawConn, []tcpopt.Option{
				tcpopt.ThinLinearTimeouts(true),
				tcpopt.ThinDupAck(true),
			})
		default:
			return nil
		}
	})
	switch {
	case err == nil:
	case errors.As(err, &kernel.ErrNoRawNetworkConn{}):
		// Non-network outputs (files, null) have no raw connection; this is expected.
		logger.Debugf(ctx, "unable to set raw network connection options: %v", err)
	default:
		logger.Errorf(ctx, "unable to set raw network connection options: %v", err)
	}
	outputKernel.Filter = packetcondition.And{
		removefiller.New(),
		s.OutputQualityMeasurer,
	}
	return outputKernel, nil
}

func (s *senderFactory) newOutput(
	ctx context.Context,
	outputTemplate SenderTemplate,
	outputURL string,
	bufSize uint,
) (_ret0 SendingNodeAbstract, _ret1 streammuxtypes.SenderConfig, _err error) {
	logger.Debugf(ctx, "newOutput(ctx, %#+v, %q, %d)", outputTemplate, outputURL, bufSize)
	defer func() {
		logger.Debugf(ctx, "/newOutput(ctx, %#+v, %q, %d): %#+v, %#+v, %v", outputTemplate, outputURL, bufSize, _ret0, _ret1, _err)
	}()

	outputKernel, err := s.newOutputKernel(ctx, outputTemplate, outputURL, bufSize)
	if err != nil {
		return nil, streammuxtypes.SenderConfig{}, fmt.Errorf("unable to create output kernel: %w", err)
	}
	outputNode := node.NewWithCustomDataFromKernel[streammux.OutputCustomData[CustomData]](ctx, outputKernel, processor.DefaultOptionsOutput()...)
	return nodeSetDropOnCloserWrapper{outputNode}, streammuxtypes.SenderConfig{}, nil
}

func (s *senderFactory) newOutputWithRetry(
	ctx context.Context,
	outputTemplate SenderTemplate,
	outputURL string,
	bufSize uint,
	retryTimeout time.Duration,
) (_ret0 SendingNodeAbstract, _ret1 streammuxtypes.SenderConfig, _err error) {
	logger.Debugf(ctx, "newOutputWithRetry(ctx, %#+v, %q, %d, %v)", outputTemplate, outputURL, bufSize, retryTimeout)
	defer func() {
		logger.Debugf(ctx, "/newOutputWithRetry(ctx, %#+v, %q, %d, %v): %#+v, %#+v, %v", outputTemplate, outputURL, bufSize, retryTimeout, _ret0, _ret1, _err)
	}()
	var errorsStartedAt time.Time
	outputKernel := kernel.NewRetryable(
		ctx,
		func(ctx context.Context) (_ret *kernel.Output, _err error) {
			outputKernel, err := s.newOutputKernel(ctx, outputTemplate, outputURL, bufSize)
			if err != nil {
				return nil, fmt.Errorf("(retryable-node:) unable to create output kernel: %w", err)
			}
			return outputKernel, nil
		},
		func(ctx context.Context, k *kernel.Output, err error) error {
			now := time.Now()
			if errorsStartedAt.IsZero() {
				errorsStartedAt = now
			}
			if now.Sub(errorsStartedAt) > retryTimeout {
				logger.Errorf(ctx, "retry timeout exceeded (%v), not retrying output anymore", retryTimeout)
				return err
			}
			logger.Debugf(ctx, "connection ended: %v", err)
			time.Sleep(100 * time.Millisecond)
			return kernel.ErrRetry{Err: err}
		},
		kernel.RetryableOptionOnKernelOpen[*kernel.Output](func(ctx context.Context, k *kernel.Output) error {
			errorsStartedAt = time.Time{}
			return nil
		}),
	)

	retryOutputNode := node.NewWithCustomDataFromKernel[streammux.OutputCustomData[CustomData]](
		ctx, outputKernel, processor.DefaultOptionsOutput()...,
	)
	return nodeWithRetrySetDropOnCloserWrapper{retryOutputNode}, streammuxtypes.SenderConfig{}, nil
}
