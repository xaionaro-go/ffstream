package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/facebookincubator/go-belt/tool/logger"
	transcodertypes "github.com/xaionaro-go/avpipeline/preset/transcoderwithpassthrough/types"
	flag "github.com/xaionaro-go/ffstream/pkg/ffflag"
)

type Flags struct {
	HWAccelGlobal         string
	Inputs                []Resource
	ListenControlSocket   string
	ListenNetPprof        string
	LoggerLevel           logger.Level
	LogstashAddr          string
	SentryDSN             string
	LogFile               string
	LockTimeout           time.Duration
	InsecureDebug         bool
	RemoveSecretsFromLogs bool
	VideoEncoder          Encoder
	AudioEncoder          Encoder
	PassthroughEncoder    bool
	PassthroughMode       transcodertypes.PassthroughMode
	Outputs               []Resource
}

type Encoder struct {
	Codec   string
	BitRate uint64
	Options []string
}

type Resource struct {
	URL     string
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
	listenControlSocket := flag.AddParameter(p, "listen_control", false, ptr(flag.String("")))
	listenNetPprof := flag.AddParameter(p, "listen_net_pprof", false, ptr(flag.String("")))
	loggerLevel := flag.AddParameter(p, "v", false, ptr(flag.LogLevel(logger.LevelInfo)))
	logstashAddr := flag.AddParameter(p, "logstash_addr", false, ptr(flag.String("")))
	sentryDSN := flag.AddParameter(p, "sentry_dsn", false, ptr(flag.String("")))
	logFile := flag.AddParameter(p, "log_file", false, ptr(flag.String("")))
	lockTimeout := flag.AddParameter(p, "lock_timeout", false, ptr(flag.Duration(time.Minute)))
	insecureDebug := flag.AddParameter(p, "insecure_debug", false, ptr(flag.Bool(false)))
	removeSecretsFromLogs := flag.AddParameter(p, "remove_secrets_from_logs", false, ptr(flag.Bool(false)))
	filterFlag := flag.AddParameter(p, "filter", false, ptr(flag.StringsAsSeparateFlags(nil)))
	filterComplexFlag := flag.AddParameter(p, "filter_complex", false, ptr(flag.StringsAsSeparateFlags(nil)))
	mapFlag := flag.AddParameter(p, "map", false, ptr(flag.StringsAsSeparateFlags(nil)))
	passthroughModeString := flag.AddParameter(p, "passthrough_mode", false, ptr(flag.String("same_tracks")))
	passthroughEncoder := flag.AddFlag(p, "passthrough_encoder", false)
	version := flag.AddFlag(p, "version", false)

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

	logger.Debugf(ctx, "unknownNonOptions: %#+v", unknownNonOptions)
	logger.Debugf(ctx, "unknownOptions: %#+v", unknownOptions)
	var outputs []Resource
	for idx, nonFlag := range unknownNonOptions {
		outputs = append(outputs, Resource{
			URL:     nonFlag,
			Options: unknownOptions[idx],
		})
	}

	var inputs []Resource
	for idx, input := range inputsFlag.Value() {
		collectedOptions := inputsFlag.CollectedUnknownOptions[idx]
		inputs = append(inputs, Resource{
			URL:     input,
			Options: collectedOptions,
		})
	}

	if len(mapFlag.Value()) != 0 {
		fatal(ctx, "mapping is not supported yet")
	}

	if len(filterFlag.Value()) != 0 {
		fatal(ctx, "filters are not supported yet")
	}

	if len(filterComplexFlag.Value()) != 0 {
		fatal(ctx, "filters are not supported yet")
	}

	passthroughMode := transcodertypes.PassthroughModeFromString(passthroughModeString.Value())
	if passthroughMode == transcodertypes.UndefinedPassthroughMode {
		fatal(ctx, "unable to parse passthrough mode", passthroughModeString)
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
		PassthroughEncoder:    passthroughEncoder.Value(),
		PassthroughMode:       passthroughMode,

		HWAccelGlobal: hwAccelFlag.Value(),
		Inputs:        inputs,
		Outputs:       outputs,
	}
	ctx = getContext(flags)

	if v := encoderBothFlag.Value(); v != "" {
		flags.AudioEncoder = Encoder{
			Codec:   v,
			Options: indexSafe(encoderBothFlag.CollectedUnknownOptions, 0),
		}
		flags.VideoEncoder = Encoder{
			Codec:   v,
			Options: indexSafe(encoderBothFlag.CollectedUnknownOptions, 0),
		}
	}

	if v := encoderVideoFlag.Value(); v != "" {
		flags.VideoEncoder = Encoder{
			Codec:   v,
			BitRate: bitrateVideoFlag.Value(),
			Options: encoderVideoFlag.CollectedUnknownOptions[0],
		}
	}

	if v := encoderAudioFlag.Value(); v != "" {
		flags.AudioEncoder = Encoder{
			Codec:   v,
			Options: encoderAudioFlag.CollectedUnknownOptions[0],
		}
	}

	return ctx, flags
}
