package goconv

import (
	"github.com/xaionaro-go/avpipeline/chain/transcoderwithpassthrough/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func RecoderConfigFromGRPC(
	req *ffstream_grpc.RecoderConfig,
) types.RecoderConfig {
	audioDeviceTypeName := types.HardwareDeviceTypeFromString(req.GetAudio().GetHardwareDeviceType())
	videoDeviceTypeName := types.HardwareDeviceTypeFromString(req.GetVideo().GetHardwareDeviceType())
	return types.RecoderConfig{
		AudioTracks: []types.TrackConfig{{
			InputTrackIDs:      []int{0, 1, 2, 3, 4, 5, 6, 7},
			CodecName:          req.GetAudio().GetCodecName(),
			AveragingPeriod:    DurationFromGRPC(int64(req.GetAudio().GetAveragingPeriod())),
			AverageBitRate:     req.GetAudio().GetAverageBitRate(),
			CustomOptions:      CustomOptionsFromGRPC(req.GetAudio().GetCustomOptions()),
			HardwareDeviceType: types.HardwareDeviceType(audioDeviceTypeName),
			HardwareDeviceName: types.HardwareDeviceName(req.GetAudio().GetHardwareDeviceName()),
		}},
		VideoTracks: []types.TrackConfig{{
			InputTrackIDs:      []int{0, 1, 2, 3, 4, 5, 6, 7},
			CodecName:          req.GetVideo().GetCodecName(),
			AveragingPeriod:    DurationFromGRPC(int64(req.GetVideo().GetAveragingPeriod())),
			AverageBitRate:     req.GetVideo().GetAverageBitRate(),
			CustomOptions:      CustomOptionsFromGRPC(req.GetVideo().GetCustomOptions()),
			HardwareDeviceType: types.HardwareDeviceType(videoDeviceTypeName),
			HardwareDeviceName: types.HardwareDeviceName(req.GetVideo().GetHardwareDeviceName()),
		}},
	}
}

func RecoderConfigToGRPC(
	cfg types.RecoderConfig,
) *ffstream_grpc.RecoderConfig {
	audio := cfg.AudioTracks[0]
	video := cfg.VideoTracks[0]
	return &ffstream_grpc.RecoderConfig{
		Audio: &ffstream_grpc.CodecConfig{
			CodecName:          audio.CodecName,
			AveragingPeriod:    uint64(DurationToGRPC(audio.AveragingPeriod)),
			AverageBitRate:     audio.AverageBitRate,
			CustomOptions:      CustomOptionsToGRPC(audio.CustomOptions),
			HardwareDeviceType: string(audio.HardwareDeviceType.String()),
			HardwareDeviceName: string(audio.HardwareDeviceName),
		},
		Video: &ffstream_grpc.CodecConfig{
			CodecName:          video.CodecName,
			AveragingPeriod:    uint64(DurationToGRPC(video.AveragingPeriod)),
			AverageBitRate:     video.AverageBitRate,
			CustomOptions:      CustomOptionsToGRPC(video.CustomOptions),
			HardwareDeviceType: string(video.HardwareDeviceType.String()),
			HardwareDeviceName: string(video.HardwareDeviceName),
		},
	}
}
