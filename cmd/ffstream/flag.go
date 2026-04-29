// flag.go defines and parses command-line flags.
package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/codec"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/preset/streammux"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	flag "github.com/xaionaro-go/ffstream/pkg/ffflag"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
)

type Flags struct {
	HWAccelGlobal               avptypes.HardwareDeviceType
	Inputs                      ffstream.Resources
	ListenControlSocket         string
	ListenNetPprof              string
	LoggerLevel                 logger.Level
	LogstashAddr                string
	SentryDSN                   string
	LogFile                     string
	LockTimeout                 time.Duration
	InsecureDebug               bool
	RemoveSecretsFromLogs       bool
	VideoEncoder                Encoder
	AudioEncoder                Encoder
	FiltersVideo                []string
	FiltersAudio                []string
	FiltersComplex              []string
	Maps                        []string
	MuxMode                     streammuxtypes.MuxMode
	AutoBitRate                 *streammuxtypes.AutoBitRateVideoConfig
	RetryInputTimeoutOnFailure  time.Duration
	RetryOutputTimeoutOnFailure time.Duration
	FrameDropVideo              bool
	FrameDropAudio              bool
	FrameDropOther              bool
	BridgePTSAcrossChains       bool
	// QueueSizeDefault is a deprecated convenience knob that, when non-
	// zero, fans out to the three per-role queue-size flags below. Use
	// QueueSizeTranscoder / QueueSizeOutput / QueueSizeError instead.
	QueueSizeDefault    uint64
	QueueSizeTranscoder uint64
	QueueSizeOutput     uint64
	QueueSizeError      uint64
	// Framerate is the value of -r (output framerate). 0 means unset;
	// main.go uses it (when non-zero) to derive a framerate-adaptive
	// default transcoder queue cap. See computeTranscoderInputCap and
	// /tmp/mission_f2_measure.md for the motivating reconfig-pause
	// budget.
	Framerate float64
	Outputs   ffstream.Resources
}

type Encoder struct {
	Codec   codec.Name
	BitRate uint64
	Options []string
}

func parseFlags(args []string) (context.Context, Flags) {
	p := flag.NewParser()
	hwAccelFlag := flag.AddParameter(p, "hwaccel", false, ptr(flag.String("none")))
	inputsFlag := flag.AddParameter(p, "i", true, ptr(flag.StringsAsSeparateFlags(nil)))
	encoderBothFlag := flag.AddParameter(p, "c", true, ptr(flag.String("copy")))
	encoderVideoFlag := flag.AddParameter(p, "c:v", true, ptr(flag.String("")))
	encoderAudioFlag := flag.AddParameter(p, "c:a", true, ptr(flag.String("")))
	bitrateVideoFlag := flag.AddParameter(p, "b:v", true, ptr(flag.Uint64(0)))
	bitrateAudioFlag := flag.AddParameter(p, "b:a", true, ptr(flag.Uint64(0)))
	listenControlSocket := flag.AddParameter(p, "listen_control", false, ptr(flag.String("")))
	listenNetPprof := flag.AddParameter(p, "listen_net_pprof", false, ptr(flag.String("")))
	loggerLevel := flag.AddParameter(p, "v", false, ptr(flag.LogLevel(logger.LevelInfo)))
	logstashAddr := flag.AddParameter(p, "logstash_addr", false, ptr(flag.String("")))
	sentryDSN := flag.AddParameter(p, "sentry_dsn", false, ptr(flag.String("")))
	logFile := flag.AddParameter(p, "log_file", false, ptr(flag.String("")))
	lockTimeout := flag.AddParameter(p, "lock_timeout", false, ptr(flag.Duration(time.Minute)))
	insecureDebug := flag.AddParameter(p, "insecure_debug", false, ptr(flag.Bool(false)))
	removeSecretsFromLogs := flag.AddParameter(p, "remove_secrets_from_logs", false, ptr(flag.Bool(false)))
	vfFlag := flag.AddParameter(p, "vf", false, ptr(flag.StringsAsSeparateFlags(nil)))
	afFlag := flag.AddParameter(p, "af", false, ptr(flag.StringsAsSeparateFlags(nil)))
	filterFlag := flag.AddParameter(p, "filter", false, ptr(flag.StringsAsSeparateFlags(nil)))
	filterComplexFlag := flag.AddParameter(p, "filter_complex", false, ptr(flag.StringsAsSeparateFlags(nil)))
	mapFlag := flag.AddParameter(p, "map", false, ptr(flag.StringsAsSeparateFlags(nil)))
	muxModeString := flag.AddParameter(p, "mux_mode", false, ptr(flag.String("forbid")))
	autoBitrate := flag.AddParameter(p, "auto_bitrate", false, ptr(flag.Bool(false)))
	autoBitrateMaxHeight := flag.AddParameter(p, "auto_bitrate_max_height", false, ptr(flag.Uint64(1080)))
	autoBitrateMinHeight := flag.AddParameter(p, "auto_bitrate_min_height", false, ptr(flag.Uint64(480)))
	autoBitrateAutoBypass := flag.AddParameter(p, "auto_bitrate_auto_bypass", false, ptr(flag.Bool(true)))
	retryInputTimeoutOnFailure := flag.AddParameter(p, "retry_input_timeout_on_failure", false, ptr(flag.Duration(ffstream.DefaultConfig().InputRetryInterval)))
	retryOutputTimeoutOnFailure := flag.AddParameter(p, "retry_output_timeout_on_failure", false, ptr(flag.Duration(0)))
	frameDropVideo := flag.AddParameter(p, "frame_drop_video", false, ptr(flag.Bool(ffstream.DefaultConfig().FrameDropVideo)))
	frameDropAudio := flag.AddParameter(p, "frame_drop_audio", false, ptr(flag.Bool(ffstream.DefaultConfig().FrameDropAudio)))
	frameDropOther := flag.AddParameter(p, "frame_drop_other", false, ptr(flag.Bool(ffstream.DefaultConfig().FrameDropOther)))
	bridgePTSAcrossChains := flag.AddParameter(p, "bridge_pts_across_chains", false, ptr(flag.Bool(ffstream.DefaultConfig().BridgePTSAcrossChains)))
	// queueSizeDefault is a deprecated convenience flag: when non-zero it
	// fans out to all three per-role flags below. Prefer the per-role
	// flags directly, since transcoder and output nodes have different
	// burst profiles (transcoder = deterministic drain, output = network-
	// bound). Sentinel 0 = leave the avpipeline compiled-in default.
	queueSizeDefault := flag.AddParameter(p, "queue_size_default", false, ptr(flag.Uint64(0)))
	queueSizeTranscoder := flag.AddParameter(p, "queue_size_transcoder", false, ptr(flag.Uint64(0)))
	queueSizeOutput := flag.AddParameter(p, "queue_size_output", false, ptr(flag.Uint64(0)))
	queueSizeError := flag.AddParameter(p, "queue_size_error", false, ptr(flag.Uint64(0)))
	// rFlag captures -r (output framerate). The value is consumed by the
	// adaptive transcoder queue cap (see main.go's call to
	// computeTranscoderInputCap). Sentinel 0 = unset.
	rFlag := flag.AddParameter(p, "r", false, ptr(flag.Float64(0)))
	reFlag := flag.AddFlag(p, "re", false)
	version := flag.AddFlag(p, "version", false)

	demuxers := flag.AddFlag(p, "demuxers", false)
	encoders := flag.AddFlag(p, "encoders", false)
	decoders := flag.AddFlag(p, "decoders", false)

	err := p.Parse(args[1:])
	ctx := getContext(Flags{
		LoggerLevel: loggerLevel.Value(),
	})
	assertNoError(ctx, err)

	if version.Value() {
		printBuildInfo(ctx, os.Stdout)
		os.Exit(0)
	}

	if demuxers.Value() {
		printDemuxers()
		os.Exit(0)
	}

	if encoders.Value() {
		printEncoders()
		os.Exit(0)
	}

	if decoders.Value() {
		printDecoders()
		os.Exit(0)
	}

	if len(p.CollectedUnknownOptions) == 0 && len(p.CollectedNonFlags) == 0 {
		fatal(ctx, "expected at least one output, but have not received any")
	}
	logger.Debugf(ctx, "p.CollectedNonFlags: %#+v", p.CollectedNonFlags)
	logger.Debugf(ctx, "p.CollectedUnknownOptions: %#+v", p.CollectedUnknownOptions)
	var unknownOptions [][]string
	var nextUnknownOptions []string
	var unknownNonOptions []string
	var nextIsOption bool
	for _, opt := range p.CollectedUnknownOptions {
		if strings.HasPrefix(opt, "-") && len(opt) != 1 {
			nextUnknownOptions = append(nextUnknownOptions, opt)
			nextIsOption = true
			continue
		}
		if nextIsOption {
			nextUnknownOptions = append(nextUnknownOptions, opt)
			nextIsOption = false
			continue
		}
		unknownOptions = append(unknownOptions, nextUnknownOptions)
		nextUnknownOptions = nil
		unknownNonOptions = append(unknownNonOptions, opt)
	}

	hardwareDeviceType := avptypes.HardwareDeviceTypeFromString(hwAccelFlag.Value())
	if hardwareDeviceType == -1 {
		logger.Errorf(ctx, "unknown hardware acceleration type %q, disabling hardware acceleration", hwAccelFlag.Value())
		hardwareDeviceType = avptypes.HardwareDeviceTypeNone
	}

	logger.Debugf(ctx, "unknownNonOptions: %#+v", unknownNonOptions)
	logger.Debugf(ctx, "unknownOptions: %#+v", unknownOptions)
	var outputs ffstream.Resources
	for idx, nonFlag := range unknownNonOptions {
		outputs = append(outputs, ffstream.Resource{
			URL:          nonFlag,
			CodecHWAccel: hardwareDeviceType,
			InputConfig: kernel.InputConfig{
				CustomOptions: convertUnknownOptionsToAVPCustomOptions(unknownOptions[idx]),
			},
		})
	}

	var inputs ffstream.Resources
	for idx, input := range inputsFlag.Value() {
		collectedOptions := inputsFlag.CollectedUnknownOptions[idx]
		opts := convertUnknownOptionsToAVPCustomOptions(collectedOptions)
		priority, opts := extractAndStripPriority(ctx, opts)
		inputConfig := kernel.InputConfig{
			ForceRealTime: ptr(reFlag.Value()),
			CustomOptions: opts,
		}
		var syncUsingReferenceAudio *int
		var suppressed bool
		for _, opt := range inputConfig.CustomOptions {
			if opt.Key == "sync_using_reference_audio" {
				val, err := strconv.Atoi(opt.Value)
				if err != nil {
					fatal(ctx, "unable to parse sync_using_reference_audio value %q: %v", opt.Value, err)
				}
				syncUsingReferenceAudio = &val
			}
			if opt.Key == "suppressed" {
				val, err := strconv.ParseBool(opt.Value)
				if err != nil {
					fatal(ctx, "unable to parse suppressed value %q: %v", opt.Value, err)
				}
				suppressed = val
			}
		}
		inputs = append(inputs, ffstream.Resource{
			URL:                     input,
			Priority:                priority,
			CodecHWAccel:            hardwareDeviceType,
			SyncUsingReferenceAudio: syncUsingReferenceAudio,
			Suppressed:              suppressed,
			InputConfig:             inputConfig,
		})
	}

	if len(filterFlag.Value()) != 0 {
		fatal(ctx, "-filter is not supported yet, use -vf/-af/-filter_complex")
	}

	muxMode := streammuxtypes.MuxModeFromString(muxModeString.Value())
	if muxMode == streammuxtypes.UndefinedMuxMode {
		fatal(ctx, "unable to parse the mux mode", muxModeString)
	}

	flags := Flags{
		ListenControlSocket: listenControlSocket.Value(),
		ListenNetPprof:      listenNetPprof.Value(),
		LoggerLevel:         loggerLevel.Value(),
		LogstashAddr:        logstashAddr.Value(),
		SentryDSN:           sentryDSN.Value(),
		LogFile:             logFile.Value(),
		LockTimeout:         lockTimeout.Value(),

		InsecureDebug:         insecureDebug.Value(),
		RemoveSecretsFromLogs: removeSecretsFromLogs.Value(),
		FiltersVideo:          vfFlag.Value(),
		FiltersAudio:          afFlag.Value(),
		FiltersComplex:        filterComplexFlag.Value(),
		Maps:                  mapFlag.Value(),
		MuxMode:               muxMode,

		RetryInputTimeoutOnFailure:  retryInputTimeoutOnFailure.Value(),
		RetryOutputTimeoutOnFailure: retryOutputTimeoutOnFailure.Value(),

		FrameDropVideo:        frameDropVideo.Value(),
		FrameDropAudio:        frameDropAudio.Value(),
		FrameDropOther:        frameDropOther.Value(),
		BridgePTSAcrossChains: bridgePTSAcrossChains.Value(),
		QueueSizeDefault:    queueSizeDefault.Value(),
		QueueSizeTranscoder: queueSizeTranscoder.Value(),
		QueueSizeOutput:     queueSizeOutput.Value(),
		QueueSizeError:      queueSizeError.Value(),
		Framerate:           rFlag.Value(),

		HWAccelGlobal: hardwareDeviceType,
		Inputs:        inputs,
		Outputs:       outputs,
	}
	ctx = getContext(flags)

	if v := encoderBothFlag.Value(); v != "" {
		flags.AudioEncoder = Encoder{
			Codec:   codec.Name(v),
			Options: indexSafe(encoderBothFlag.CollectedUnknownOptions, 0),
		}
		flags.VideoEncoder = Encoder{
			Codec:   codec.Name(v),
			Options: indexSafe(encoderBothFlag.CollectedUnknownOptions, 0),
		}
	}

	if v := encoderVideoFlag.Value(); v != "" {
		flags.VideoEncoder = Encoder{
			Codec:   codec.Name(v),
			BitRate: bitrateVideoFlag.Value(),
			Options: indexSafe(encoderVideoFlag.CollectedUnknownOptions, 0),
		}
	}

	if v := encoderAudioFlag.Value(); v != "" {
		flags.AudioEncoder = Encoder{
			Codec:   codec.Name(v),
			BitRate: bitrateAudioFlag.Value(),
			Options: indexSafe(encoderAudioFlag.CollectedUnknownOptions, 0),
		}
	}

	if autoBitrate.Value() {
		logger.Tracef(ctx, "enabling auto bitrate")
		vCodec := flags.VideoEncoder.Codec.Codec(ctx, true)
		if vCodec == nil {
			fatal(ctx, "unable to determine video codec from %q", flags.VideoEncoder.Codec)
		}
		cfg, err := streammux.DefaultAutoBitRateVideoConfig(vCodec.ID())
		if err != nil {
			fatal(ctx, "unable to get default auto-bitrate config: %v", err)
		}
		// Use AllowedResolutionsAndBitRates() (which falls back to the full
		// set when min/max filtering would produce empty) instead of
		// directly mutating ResolutionsAndBitRates with MaxHeight/MinHeight,
		// because some codec configs (e.g. AV1) collapse to a single high-
		// resolution entry that gets nuked by a 1080p MaxHeight filter,
		// leading to a nil-deref in Best() further below.
		cfg.MaxResolution = codec.Resolution{Height: uint32(autoBitrateMaxHeight.Value())}
		cfg.MinResolution = codec.Resolution{Height: uint32(autoBitrateMinHeight.Value())}
		if flags.MuxMode == streammuxtypes.MuxModeForbid {
			allowed := cfg.AllowedResolutionsAndBitRates()
			cfg.ResolutionsAndBitRates = streammuxtypes.AutoBitRateResolutionAndBitRateConfigs{
				*allowed.Best(),
			}
		}
		cfg.AutoByPass = autoBitrateAutoBypass.Value()
		allowed := cfg.AllowedResolutionsAndBitRates()
		cfg.MaxBitRate = allowed.Best().BitrateHigh
		cfg.MinBitRate = allowed.Worst().BitrateLow
		flags.AutoBitRate = &cfg
	}

	return ctx, flags
}

func extractAndStripPriority(
	ctx context.Context,
	opts avptypes.DictionaryItems,
) (uint, avptypes.DictionaryItems) {
	var priority uint
	result := make(avptypes.DictionaryItems, 0, len(opts))
	for _, item := range opts {
		if item.Key != "fallback_priority" {
			result = append(result, item)
			continue
		}
		v, err := strconv.ParseUint(item.Value, 10, 0)
		if err != nil {
			logger.Errorf(ctx, "unable to parse fallback priority %q: %v", item.Value, err)
			continue
		}
		priority = uint(v)
	}
	if len(result) == 0 {
		return priority, nil
	}
	return priority, result
}
