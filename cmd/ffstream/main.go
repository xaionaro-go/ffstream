package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	child_process_manager "github.com/AgustinSRG/go-child-process-manager"
	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/kernel"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstream/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver"
	"github.com/xaionaro-go/observability"
	"github.com/xaionaro-go/secret"
	"github.com/xaionaro-go/xsync"
)

func main() {
	err := child_process_manager.InitializeChildProcessManager()
	if err != nil {
		panic(err)
	}
	defer child_process_manager.DisposeChildProcessManager()

	flags := parseFlags(context.TODO(), os.Args)
	ctx := getContext(flags)

	ctx, cancelFunc := initRuntime(ctx, flags)
	defer cancelFunc()
	ctx = xsync.WithNoLogging(ctx, true)

	logger.Debugf(ctx, "flags == %#+v", flags)

	s := ffstream.New(ctx)

	if flags.ListenControlSocket != "" {
		logger.Debugf(ctx, "flags.ListenControlSocket == '%s'", flags.ListenControlSocket)
		listener, err := getListener(ctx, flags.ListenControlSocket)
		assertNoError(ctx, err)

		observability.Go(ctx, func() {
			logger.Infof(ctx, "listening for gRPC clients at %s (%T)", listener.Addr(), listener)
			ffstreamserver.New(s).ServeContext(ctx, listener)
		})
	}

	for _, input := range flags.Inputs {
		opts := convertUnknownOptionsToAVPCustomOptions(input.Options)
		logger.Debugf(ctx, "input %s opts: %v", input.URL, opts)
		input, err := kernel.NewInputFromURL(ctx, input.URL, secret.New(""), kernel.InputConfig{
			CustomOptions: opts,
		})
		assertNoError(ctx, err)
		s.AddInput(ctx, input)
	}

	outputOptions := convertUnknownOptionsToAVPCustomOptions(flags.Output.Options)
	var outputFormat string
	for _, v := range outputOptions {
		switch v.Key {
		case "f":
			outputFormat = v.Value
		}
	}
	if outputFormat == "mpegts" {
		var movFlags *avptypes.DictionaryItem
		for idx, item := range outputOptions {
			if item.Key == "movflags" {
				movFlags = &outputOptions[idx]
				break
			}
		}
		if movFlags == nil {
			outputOptions = append(outputOptions, avptypes.DictionaryItem{Key: "movflags"})
			movFlags = &outputOptions[len(outputOptions)-1]
		}
		if movFlags.Value != "" {
			movFlags.Value += "+"
		}
		movFlags.Value += "frag_keyframe+empty_moov+separate_moof"
	}
	output, err := kernel.NewOutputFromURL(ctx, flags.Output.URL, secret.New(""), kernel.OutputConfig{
		CustomOptions: outputOptions,
	})
	assertNoError(ctx, err)
	s.AddOutput(ctx, output)

	encoderVideoOptions := convertUnknownOptionsToCustomOptions(flags.VideoEncoder.Options)
	for _, v := range outputOptions {
		switch v.Key {
		case "g", "r", "bufsize":
			encoderVideoOptions = append(encoderVideoOptions, types.DictionaryItem{
				Key:   v.Key,
				Value: v.Value,
			})
		}
	}

	err = s.SetRecoderConfig(ctx, types.RecoderConfig{
		Audio: types.CodecConfig{
			CodecName:     flags.AudioEncoder.Codec,
			CustomOptions: convertUnknownOptionsToCustomOptions(flags.AudioEncoder.Options),
		},
		Video: types.CodecConfig{
			CodecName:          flags.VideoEncoder.Codec,
			CustomOptions:      encoderVideoOptions,
			HardwareDeviceName: types.HardwareDeviceName(flags.HWAccelGlobal),
		},
	})
	assertNoError(ctx, err)

	if flags.PassthroughEncoder {
		logger.Infof(ctx, "passing through the encoder due to the flag provided")
		s.PassthroughSwitch.CurrentValue.Store(1)
	}

	err = s.Start(ctx)
	assertNoError(ctx, err)

	if logger.FromCtx(ctx).Level() >= logger.LevelDebug {
		observability.Go(ctx, func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				stats := s.GetAllStats(ctx)
				statsBytes, err := json.Marshal(stats)
				if err != nil {
					logger.Errorf(ctx, "unable to JSON-ize the statistics: %v", err)
				}
				logger.Debugf(ctx, "%s", statsBytes)
			}
		})
	}

	err = s.Wait(ctx)
	assertNoError(ctx, err)
}
