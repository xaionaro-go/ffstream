package goconv

import (
	"github.com/xaionaro-go/avpipeline/preset/transcoderwithpassthrough/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func RecoderConfigFromGRPC(
	req *ffstream_grpc.RecoderConfig,
) types.RecoderConfig {
	audioDeviceTypeName := types.HardwareDeviceTypeFromString(req.GetAudio().GetHardwareDeviceType())
	videoDeviceTypeName := types.HardwareDeviceTypeFromString(req.GetVideo().GetHardwareDeviceType())
	return types.RecoderConfig{
		VideoTrackConfigs: []types.TrackConfig{{
			InputTrackIDs:      []int{0, 1, 2, 3, 4, 5, 6, 7},
			OutputTrackIDs:     []int{0},
			CodecName:          req.GetVideo().GetCodecName(),
			AveragingPeriod:    DurationFromGRPC(int64(req.GetVideo().GetAveragingPeriod())),
			AverageBitRate:     req.GetVideo().GetAverageBitRate(),
			CustomOptions:      CustomOptionsFromGRPC(req.GetVideo().GetCustomOptions()),
			HardwareDeviceType: types.HardwareDeviceType(videoDeviceTypeName),
			HardwareDeviceName: types.HardwareDeviceName(req.GetVideo().GetHardwareDeviceName()),
		}},
		AudioTrackConfigs: []types.TrackConfig{{
			InputTrackIDs:      []int{0, 1, 2, 3, 4, 5, 6, 7},
			OutputTrackIDs:     []int{1},
			CodecName:          req.GetAudio().GetCodecName(),
			AveragingPeriod:    DurationFromGRPC(int64(req.GetAudio().GetAveragingPeriod())),
			AverageBitRate:     req.GetAudio().GetAverageBitRate(),
			CustomOptions:      CustomOptionsFromGRPC(req.GetAudio().GetCustomOptions()),
			HardwareDeviceType: types.HardwareDeviceType(audioDeviceTypeName),
			HardwareDeviceName: types.HardwareDeviceName(req.GetAudio().GetHardwareDeviceName()),
		}},
	}
}

func RecoderConfigToGRPC(
	cfg types.RecoderConfig,
) *ffstream_grpc.RecoderConfig {
	result := &ffstream_grpc.RecoderConfig{}
	if len(cfg.AudioTrackConfigs) > 0 {
		audio := cfg.AudioTrackConfigs[0]
		result.Audio = &ffstream_grpc.CodecConfig{
			CodecName:          audio.CodecName,
			AveragingPeriod:    uint64(DurationToGRPC(audio.AveragingPeriod)),
			AverageBitRate:     audio.AverageBitRate,
			CustomOptions:      CustomOptionsToGRPC(audio.CustomOptions),
			HardwareDeviceType: string(audio.HardwareDeviceType.String()),
			HardwareDeviceName: string(audio.HardwareDeviceName),
		}
	}
	if len(cfg.VideoTrackConfigs) > 0 {
		video := cfg.VideoTrackConfigs[0]
		result.Video = &ffstream_grpc.CodecConfig{
			CodecName:          video.CodecName,
			AveragingPeriod:    uint64(DurationToGRPC(video.AveragingPeriod)),
			AverageBitRate:     video.AverageBitRate,
			CustomOptions:      CustomOptionsToGRPC(video.CustomOptions),
			HardwareDeviceType: string(video.HardwareDeviceType.String()),
			HardwareDeviceName: string(video.HardwareDeviceName),
		}
	}
	return result
}
