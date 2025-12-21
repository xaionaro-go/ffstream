package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	child_process_manager "github.com/AgustinSRG/go-child-process-manager"
	"github.com/facebookincubator/go-belt/tool/logger"
	audio "github.com/xaionaro-go/audio/pkg/audio/types"
	"github.com/xaionaro-go/avpipeline/codec"
	codectypes "github.com/xaionaro-go/avpipeline/codec/types"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver"
	"github.com/xaionaro-go/observability"
	"github.com/xaionaro-go/xsync"
)

func main() {
	err := child_process_manager.InitializeChildProcessManager()
	if err != nil {
		panic(err)
	}
	defer child_process_manager.DisposeChildProcessManager()

	ctx, flags := parseFlags(os.Args)

	ctx, cancelFunc := initRuntime(ctx, flags)
	defer cancelFunc()
	ctx = xsync.WithNoLogging(ctx, true)

	logger.Debugf(ctx, "flags == %#+v", flags)

	platformInit()
	logger.Debugf(ctx, "platform initialized")

	s, err := ffstream.New(ctx,
		ffstream.OptionInputRetryIntervalValue(flags.RetryInputTimeoutOnFailure),
	)
	assertNoError(ctx, err)

	if flags.ListenControlSocket != "" {
		logger.Debugf(ctx, "flags.ListenControlSocket == '%s'", flags.ListenControlSocket)
		listener, err := getListener(ctx, flags.ListenControlSocket)
		assertNoError(ctx, err)

		observability.Go(ctx, func(ctx context.Context) {
			logger.Infof(ctx, "listening for gRPC clients at %s (%T)", listener.Addr(), listener)
			ffstreamserver.New(s).ServeContext(ctx, listener)
		})
	}

	for _, inputInfo := range flags.Inputs {
		err = s.AddInput(ctx, inputInfo)
		assertNoError(ctx, err)
	}

	var resolution codec.Resolution
	var audioSampleRate audio.SampleRate

	var encoderVideoOptions avptypes.DictionaryItems
	encoderVideoOptions = append(encoderVideoOptions,
		codec.LowLatencyOptions(ctx, flags.VideoEncoder.Codec, true)...,
	)
	encoderVideoOptions = append(encoderVideoOptions,
		convertUnknownOptionsToCustomOptions(flags.VideoEncoder.Options)...,
	)
	encoderVideoOptions = encoderVideoOptions.Deduplicate()

	for idx, v := range encoderVideoOptions {
		logger.Tracef(ctx, "encoderVideoOptions[%d]: %s=%s", idx, v.Key, v.Value)
		if len(v.Key) == 0 {
			logger.Fatalf(ctx, "unexpected empty output option key with value %q", v.Value)
		}
		switch v.Key {
		case "s":
			_, err := fmt.Sscanf(v.Value, "%dx%d", &resolution.Width, &resolution.Height)
			assertNoError(ctx, err)
			logger.Debugf(ctx, "parsed resolution: %dx%d", resolution.Width, resolution.Height)
		}
	}

	var encoderAudioOptions avptypes.DictionaryItems
	encoderAudioOptions = append(encoderAudioOptions,
		convertUnknownOptionsToCustomOptions(flags.AudioEncoder.Options)...,
	)
	encoderAudioOptions = encoderAudioOptions.Deduplicate()

	for idx, v := range encoderAudioOptions {
		logger.Tracef(ctx, "encoderAudioOptions[%d]: %s=%s", idx, v.Key, v.Value)
		if len(v.Key) == 0 {
			logger.Fatalf(ctx, "unexpected empty output option key with value %q", v.Value)
		}
		switch v.Key {
		case "ar":
			must(fmt.Sscanf(v.Value, "%d", &audioSampleRate))
			logger.Debugf(ctx, "parsed audio sample rate: %d", audioSampleRate)
		}
	}

	for _, outputParams := range flags.Outputs {
		logger.Debugf(ctx, "outputParams == %#+v", outputParams)
		outputOptions := outputParams.CustomOptions
		var outputFormat string
		for _, v := range outputOptions {
			switch v.Key {
			case "-f":
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

		err := s.AddOutputTemplate(ctx, ffstream.SenderTemplate{
			URLTemplate:                 outputParams.URL,
			Options:                     outputOptions,
			RetryOutputTimeoutOnFailure: flags.RetryOutputTimeoutOnFailure,
		})
		assertNoError(ctx, err)
	}

	hardwareDeviceType := avptypes.HardwareDeviceTypeFromString(flags.HWAccelGlobal)
	if hardwareDeviceType == -1 {
		hardwareDeviceType = avptypes.HardwareDeviceTypeNone
	}
	transcoderConfig := streammuxtypes.TranscoderConfig{
		Output: streammuxtypes.TranscoderOutputConfig{
			VideoTrackConfigs: []streammuxtypes.OutputVideoTrackConfig{{
				InputTrackIDs:      []int{0, 1, 2, 3, 4, 5, 6, 7},
				OutputTrackIDs:     []int{0},
				CodecName:          codectypes.Name(flags.VideoEncoder.Codec),
				AverageBitRate:     flags.VideoEncoder.BitRate,
				CustomOptions:      encoderVideoOptions,
				HardwareDeviceType: hardwareDeviceType,
				Resolution: codec.Resolution{
					Width:  resolution.Width,
					Height: resolution.Height,
				},
			}},
			AudioTrackConfigs: []streammuxtypes.OutputAudioTrackConfig{{
				InputTrackIDs:  []int{0, 1, 2, 3, 4, 5, 6, 7},
				OutputTrackIDs: []int{1},
				CodecName:      codectypes.Name(flags.AudioEncoder.Codec),
				AverageBitRate: flags.AudioEncoder.BitRate,
				CustomOptions:  convertUnknownOptionsToCustomOptions(flags.AudioEncoder.Options),
				SampleRate:     audioSampleRate,
			}},
		},
	}

	err = s.Start(ctx, transcoderConfig, flags.MuxMode, flags.AutoBitRate)
	assertNoError(ctx, err)

	if logger.FromCtx(ctx).Level() >= logger.LevelTrace {
		observability.Go(ctx, func(ctx context.Context) {
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
				logger.Tracef(ctx, "%s", statsBytes)
			}
		})
	}

	err = s.Wait(ctx)
	assertNoError(ctx, err)

	logger.Infof(ctx, "finished")
}
