// main.go is the entry point for the ffstream command-line tool.

// Package main is the entry point for the ffstream command-line tool.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
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

	codec.FallbackToSoftwareOnNoHWCodec = true

	ctx, flags := parseFlags(os.Args)

	ctx, cancelFunc := initRuntime(ctx, flags)
	defer cancelFunc()

	ctx, sigStop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer sigStop()

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

	// When no explicit output resolution (-s WxH) is given, default to the
	// first input's video_size so the encoder matches the source resolution.
	var resolution codec.Resolution
	for _, inputInfo := range flags.Inputs {
		for _, opt := range inputInfo.CustomOptions {
			if opt.Key == "video_size" {
				_, err := fmt.Sscanf(opt.Value, "%dx%d", &resolution.Width, &resolution.Height)
				assertNoError(ctx, err)
				break
			}
		}
		if resolution != (codec.Resolution{}) {
			break
		}
	}

	var audioSampleRate audio.SampleRate = 48000
	var audioChannels audio.Channel

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
			// Explicit -s overrides the input-derived default.
			_, err := fmt.Sscanf(v.Value, "%dx%d", &resolution.Width, &resolution.Height)
			assertNoError(ctx, err)
			logger.Debugf(ctx, "parsed resolution: %dx%d", resolution.Width, resolution.Height)
		}
	}

	// Strip -s from video custom options: it is carried as the structured
	// Resolution field and would otherwise be double-applied by the
	// encoder factory.
	videoCustomOptions := slices.DeleteFunc(encoderVideoOptions, func(item avptypes.DictionaryItem) bool {
		return item.Key == "s"
	})

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
		case "ac":
			must(fmt.Sscanf(v.Value, "%d", &audioChannels))
			logger.Debugf(ctx, "parsed audio channels: %d", audioChannels)
		}
	}

	// Strip -ar/-ac from audio custom options: they are carried as the
	// structured SampleRate/Channels fields and would otherwise be
	// double-applied by the encoder factory.
	audioCustomOptions := slices.DeleteFunc(encoderAudioOptions, func(item avptypes.DictionaryItem) bool {
		switch item.Key {
		case "ar", "ac":
			return true
		}
		return false
	})

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
		// adding options required for fragmentation (that is a streaming-specific issue)
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

	var videoInputTrackIDs []int
	var audioInputTrackIDs []int
	if len(flags.Maps) > 0 {
		for _, m := range flags.Maps {
			// for now we only support simple numeric mapping for "analog"
			idx, err := strconv.Atoi(m)
			if err == nil {
				videoInputTrackIDs = append(videoInputTrackIDs, idx)
				audioInputTrackIDs = append(audioInputTrackIDs, idx)
			} else {
				logger.Warnf(ctx, "unsupported map format %q: only numeric indices are supported for now", m)
			}
		}
	}
	if len(videoInputTrackIDs) == 0 {
		videoInputTrackIDs = []int{0, 1, 2, 3, 4, 5, 6, 7}
	}
	if len(audioInputTrackIDs) == 0 {
		audioInputTrackIDs = []int{0, 1, 2, 3, 4, 5, 6, 7}
	}

	transcoderConfig := streammuxtypes.TranscoderConfig{
		Output: streammuxtypes.TranscoderOutputConfig{
			FilterComplex: strings.Join(flags.FiltersComplex, ","),
			VideoTrackConfigs: []streammuxtypes.OutputVideoTrackConfig{{
				InputTrackIDs:      videoInputTrackIDs,
				OutputTrackIDs:     []int{0},
				Filters:            flags.FiltersVideo,
				CodecName:          codectypes.Name(flags.VideoEncoder.Codec),
				AverageBitRate:     flags.VideoEncoder.BitRate,
				CustomOptions:      videoCustomOptions,
				HardwareDeviceType: flags.HWAccelGlobal,
				Resolution: codec.Resolution{
					Width:  resolution.Width,
					Height: resolution.Height,
				},
			}},
			AudioTrackConfigs: []streammuxtypes.OutputAudioTrackConfig{{
				InputTrackIDs:  audioInputTrackIDs,
				OutputTrackIDs: []int{1},
				Filters:        flags.FiltersAudio,
				CodecName:      codectypes.Name(flags.AudioEncoder.Codec),
				AverageBitRate: flags.AudioEncoder.BitRate,
				CustomOptions:  audioCustomOptions,
				SampleRate:     audioSampleRate,
				Channels:       audioChannels,
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
