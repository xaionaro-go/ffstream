package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	child_process_manager "github.com/AgustinSRG/go-child-process-manager"
	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/codec"
	"github.com/xaionaro-go/avpipeline/kernel"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
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

	ctx, flags := parseFlags(os.Args)

	ctx, cancelFunc := initRuntime(ctx, flags)
	defer cancelFunc()
	ctx = xsync.WithNoLogging(ctx, true)

	logger.Debugf(ctx, "flags == %#+v", flags)

	s := ffstream.New(ctx)

	if flags.ListenControlSocket != "" {
		logger.Debugf(ctx, "flags.ListenControlSocket == '%s'", flags.ListenControlSocket)
		listener, err := getListener(ctx, flags.ListenControlSocket)
		assertNoError(ctx, err)

		observability.Go(ctx, func(ctx context.Context) {
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

	var resolution codec.Resolution
	encoderVideoOptions := convertUnknownOptionsToCustomOptions(flags.VideoEncoder.Options)

	for _, outputParams := range flags.Outputs {
		logger.Debugf(ctx, "outputParams == %#+v", outputParams)
		outputOptions := convertUnknownOptionsToAVPCustomOptions(outputParams.Options)
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

		err := s.AddOutputTemplate(ctx, ffstream.OutputTemplate{
			URLTemplate: outputParams.URL,
			Options:     outputOptions,
		})
		assertNoError(ctx, err)

		for _, v := range outputOptions {
			logger.Tracef(ctx, "outputOptions[%d] == %#+v", v.Key, v)
			switch v.Key {
			case "-s":
				_, err := fmt.Sscanf(v.Value, "%dx%d", &resolution.Width, &resolution.Height)
				assertNoError(ctx, err)
			case "-g", "-r", "-bufsize":
				encoderVideoOptions = append(encoderVideoOptions, streammuxtypes.DictionaryItem{
					Key:   v.Key,
					Value: v.Value,
				})
			}
		}
	}

	hardwareDeviceType := streammuxtypes.HardwareDeviceTypeFromString(flags.HWAccelGlobal)
	if hardwareDeviceType == -1 {
		hardwareDeviceType = streammuxtypes.HardwareDeviceTypeNone
	}
	recoderConfig := streammuxtypes.RecoderConfig{
		VideoTrackConfigs: []streammuxtypes.VideoTrackConfig{{
			InputTrackIDs:      []int{0, 1, 2, 3, 4, 5, 6, 7},
			OutputTrackIDs:     []int{0},
			CodecName:          flags.VideoEncoder.Codec,
			AverageBitRate:     flags.VideoEncoder.BitRate,
			CustomOptions:      encoderVideoOptions,
			HardwareDeviceType: hardwareDeviceType,
			Width:              resolution.Width,
			Height:             resolution.Height,
		}},
		AudioTrackConfigs: []streammuxtypes.AudioTrackConfig{{
			InputTrackIDs:  []int{0, 1, 2, 3, 4, 5, 6, 7},
			OutputTrackIDs: []int{1},
			CodecName:      flags.AudioEncoder.Codec,
			AverageBitRate: flags.AudioEncoder.BitRate,
			CustomOptions:  convertUnknownOptionsToCustomOptions(flags.AudioEncoder.Options),
		}},
	}

	err = s.Start(ctx, recoderConfig, flags.MuxMode)
	assertNoError(ctx, err)

	if logger.FromCtx(ctx).Level() >= logger.LevelDebug {
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
				logger.Debugf(ctx, "%s", statsBytes)
			}
		})
	}

	err = s.Wait(ctx)
	assertNoError(ctx, err)
}
