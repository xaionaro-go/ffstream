package main

import (
	"context"
	"os"

	child_process_manager "github.com/AgustinSRG/go-child-process-manager"
	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/kernel"
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
		input, err := kernel.NewInputFromURL(ctx, input.URL, secret.New(""), kernel.InputConfig{
			CustomOptions: convertUnknownOptionsToAVPCustomOptions(input.Options),
		})
		assertNoError(ctx, err)
		s.AddInput(ctx, input)
	}

	outputOptions := convertUnknownOptionsToAVPCustomOptions(flags.Output.Options)
	encoderVideoOptions := convertUnknownOptionsToCustomOptions(flags.VideoEncoder.Options)

	output, err := kernel.NewOutputFromURL(ctx, flags.Output.URL, secret.New(""), kernel.OutputConfig{
		CustomOptions: outputOptions,
	})
	assertNoError(ctx, err)
	s.AddOutput(ctx, output)

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
		s.Switch.KernelIndex.Store(1)
	}

	err = s.Start(ctx)
	assertNoError(ctx, err)

	err = s.Wait(ctx)
	assertNoError(ctx, err)
}
