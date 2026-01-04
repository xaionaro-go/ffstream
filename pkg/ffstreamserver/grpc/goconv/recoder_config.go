// recoder_config.go provides conversion functions for transcoder configuration between GRPC and Go.

// Package goconv provides conversion functions between GRPC and Go for ffstreamserver.
package goconv

import (
	audiotypes "github.com/xaionaro-go/audio/pkg/audio/types"
	codectypes "github.com/xaionaro-go/avpipeline/codec/types"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func TranscoderConfigFromGRPC(
	req *ffstream_grpc.TranscoderConfig,
) streammuxtypes.TranscoderConfig {
	videoDeviceTypeName := avptypes.HardwareDeviceTypeFromString(req.GetVideo().GetHardwareDeviceType())
	return streammuxtypes.TranscoderConfig{
		Output: streammuxtypes.TranscoderOutputConfig{
			VideoTrackConfigs: []streammuxtypes.OutputVideoTrackConfig{{
				InputTrackIDs:      []int{0, 1, 2, 3, 4, 5, 6, 7},
				OutputTrackIDs:     []int{0},
				CodecName:          codectypes.Name(req.GetVideo().GetCodecName()),
				AveragingPeriod:    DurationFromGRPC(int64(req.GetVideo().GetAveragingPeriod())),
				AverageBitRate:     req.GetVideo().GetAverageBitRate(),
				CustomOptions:      CustomOptionsFromGRPC(req.GetVideo().GetCustomOptions()),
				HardwareDeviceType: streammuxtypes.HardwareDeviceType(videoDeviceTypeName),
				HardwareDeviceName: streammuxtypes.HardwareDeviceName(req.GetVideo().GetHardwareDeviceName()),
				Resolution: codectypes.Resolution{
					Width:  req.GetVideo().GetWidth(),
					Height: req.GetVideo().GetHeight(),
				},
			}},
			AudioTrackConfigs: []streammuxtypes.OutputAudioTrackConfig{{
				InputTrackIDs:   []int{0, 1, 2, 3, 4, 5, 6, 7},
				OutputTrackIDs:  []int{1},
				CodecName:       codectypes.Name(req.GetAudio().GetCodecName()),
				AveragingPeriod: DurationFromGRPC(int64(req.GetAudio().GetAveragingPeriod())),
				AverageBitRate:  req.GetAudio().GetAverageBitRate(),
				CustomOptions:   CustomOptionsFromGRPC(req.GetAudio().GetCustomOptions()),
				SampleRate:      audiotypes.SampleRate(req.GetAudio().GetSampleRate()),
			}},
		},
	}
}

func TranscoderConfigToGRPC(
	cfg streammuxtypes.TranscoderConfig,
) *ffstream_grpc.TranscoderConfig {
	result := &ffstream_grpc.TranscoderConfig{}
	if len(cfg.Output.AudioTrackConfigs) > 0 {
		audio := cfg.Output.AudioTrackConfigs[0]
		result.Audio = &ffstream_grpc.AudioCodecConfig{
			CodecName:       string(audio.CodecName),
			AveragingPeriod: uint64(DurationToGRPC(audio.AveragingPeriod)),
			AverageBitRate:  audio.AverageBitRate,
			CustomOptions:   CustomOptionsToGRPC(audio.CustomOptions),
			SampleRate:      uint32(audio.SampleRate),
		}
	}
	if len(cfg.Output.VideoTrackConfigs) > 0 {
		video := cfg.Output.VideoTrackConfigs[0]
		result.Video = &ffstream_grpc.VideoCodecConfig{
			CodecName:          string(video.CodecName),
			AveragingPeriod:    uint64(DurationToGRPC(video.AveragingPeriod)),
			AverageBitRate:     video.AverageBitRate,
			CustomOptions:      CustomOptionsToGRPC(video.CustomOptions),
			HardwareDeviceType: string(video.HardwareDeviceType.String()),
			HardwareDeviceName: string(video.HardwareDeviceName),
			Width:              video.Resolution.Width,
			Height:             video.Resolution.Height,
		}
	}
	return result
}
