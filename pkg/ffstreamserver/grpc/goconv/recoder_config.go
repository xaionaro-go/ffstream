package goconv

import (
	codectypes "github.com/xaionaro-go/avpipeline/codec/types"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func RecoderConfigFromGRPC(
	req *ffstream_grpc.RecoderConfig,
) streammuxtypes.RecoderConfig {
	videoDeviceTypeName := avptypes.HardwareDeviceTypeFromString(req.GetVideo().GetHardwareDeviceType())
	return streammuxtypes.RecoderConfig{
		VideoTrackConfigs: []streammuxtypes.VideoTrackConfig{{
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
		AudioTrackConfigs: []streammuxtypes.AudioTrackConfig{{
			InputTrackIDs:   []int{0, 1, 2, 3, 4, 5, 6, 7},
			OutputTrackIDs:  []int{1},
			CodecName:       codectypes.Name(req.GetAudio().GetCodecName()),
			AveragingPeriod: DurationFromGRPC(int64(req.GetAudio().GetAveragingPeriod())),
			AverageBitRate:  req.GetAudio().GetAverageBitRate(),
			CustomOptions:   CustomOptionsFromGRPC(req.GetAudio().GetCustomOptions()),
		}},
	}
}

func RecoderConfigToGRPC(
	cfg streammuxtypes.RecoderConfig,
) *ffstream_grpc.RecoderConfig {
	result := &ffstream_grpc.RecoderConfig{}
	if len(cfg.AudioTrackConfigs) > 0 {
		audio := cfg.AudioTrackConfigs[0]
		result.Audio = &ffstream_grpc.AudioCodecConfig{
			CodecName:       string(audio.CodecName),
			AveragingPeriod: uint64(DurationToGRPC(audio.AveragingPeriod)),
			AverageBitRate:  audio.AverageBitRate,
			CustomOptions:   CustomOptionsToGRPC(audio.CustomOptions),
		}
	}
	if len(cfg.VideoTrackConfigs) > 0 {
		video := cfg.VideoTrackConfigs[0]
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
