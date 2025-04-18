package main

import (
	"context"
	"os"
	"time"

	"github.com/facebookincubator/go-belt/tool/logger"
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
	Output                Resource
}

type Encoder struct {
	Codec   string
	Options []string
}

type Resource struct {
	URL     string
	Options []string
}

func parseFlags(ctx context.Context, args []string) Flags {
	p := flag.NewParser()
	hwAccelFlag := flag.AddParameter(p, "hwaccel", false, ptr(flag.String("none")))
	inputsFlag := flag.AddParameter(p, "i", true, ptr(flag.StringsAsSeparateFlags(nil)))
	encoderBothFlag := flag.AddParameter(p, "c", true, ptr(flag.String("copy")))
	encoderVideoFlag := flag.AddParameter(p, "c:v", true, ptr(flag.String("")))
	encoderAudioFlag := flag.AddParameter(p, "c:a", true, ptr(flag.String("")))
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
	passthroughEncoder := flag.AddFlag(p, "passthrough_encoder", false)
	version := flag.AddFlag(p, "version", false)

	encoders := flag.AddFlag(p, "encoders", false)
	decoders := flag.AddFlag(p, "decoders", false)

	err := p.Parse(args[1:])
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
		fatal(ctx, "expected one output, but have not received any")
	}
	if len(p.CollectedNonFlags) > 1 {
		fatal(ctx, "expected one output, but received %d", len(p.CollectedNonFlags))
	}
	if len(p.CollectedNonFlags) == 0 {
		p.CollectedNonFlags = p.CollectedUnknownOptions[len(p.CollectedUnknownOptions)-1:]
		p.CollectedUnknownOptions = p.CollectedUnknownOptions[:len(p.CollectedUnknownOptions)-1]
	}
	output := Resource{
		URL:     p.CollectedNonFlags[0],
		Options: p.CollectedUnknownOptions,
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

		HWAccelGlobal: hwAccelFlag.Value(),
		Inputs:        inputs,
		Output:        output,
	}

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
			Options: encoderVideoFlag.CollectedUnknownOptions[0],
		}
	}

	if v := encoderAudioFlag.Value(); v != "" {
		flags.AudioEncoder = Encoder{
			Codec:   v,
			Options: encoderAudioFlag.CollectedUnknownOptions[0],
		}
	}

	return flags
}
