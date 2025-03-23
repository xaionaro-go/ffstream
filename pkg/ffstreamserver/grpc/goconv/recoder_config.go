package goconv

import (
	"github.com/asticode/go-astiav"
	"github.com/xaionaro-go/avpipeline/codec"
	"github.com/xaionaro-go/ffstream/pkg/ffstream/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func RecoderConfigFromGRPC(
	req *ffstream_grpc.RecoderConfig,
) types.RecoderConfig {
	audioDeviceTypeName := astiav.FindHardwareDeviceTypeByName(req.GetAudio().GetHardwareDeviceType())
	videoDeviceTypeName := astiav.FindHardwareDeviceTypeByName(req.GetVideo().GetHardwareDeviceType())
	return types.RecoderConfig{
		Audio: types.CodecConfig{
			CodecName:          req.GetAudio().GetCodecName(),
			AveragingPeriod:    DurationFromGRPC(int64(req.GetAudio().GetAveragingPeriod())),
			AverageBitRate:     req.GetAudio().GetAverageBitRate(),
			CustomOptions:      CustomOptionsFromGRPC(req.GetAudio().GetCustomOptions()),
			HardwareDeviceType: codec.HardwareDeviceType(audioDeviceTypeName),
			HardwareDeviceName: codec.HardwareDeviceName(req.GetAudio().GetHardwareDeviceName()),
		},
		Video: types.CodecConfig{
			CodecName:          req.GetVideo().GetCodecName(),
			AveragingPeriod:    DurationFromGRPC(int64(req.GetVideo().GetAveragingPeriod())),
			AverageBitRate:     req.GetVideo().GetAverageBitRate(),
			CustomOptions:      CustomOptionsFromGRPC(req.GetVideo().GetCustomOptions()),
			HardwareDeviceType: codec.HardwareDeviceType(videoDeviceTypeName),
			HardwareDeviceName: codec.HardwareDeviceName(req.GetVideo().GetHardwareDeviceName()),
		},
	}
}

func RecoderConfigToGRPC(
	cfg types.RecoderConfig,
) *ffstream_grpc.RecoderConfig {
	return &ffstream_grpc.RecoderConfig{
		Audio: &ffstream_grpc.CodecConfig{
			CodecName:          cfg.Audio.CodecName,
			AveragingPeriod:    uint64(DurationToGRPC(cfg.Audio.AveragingPeriod)),
			AverageBitRate:     cfg.Audio.AverageBitRate,
			CustomOptions:      CustomOptionsToGRPC(cfg.Audio.CustomOptions),
			HardwareDeviceType: string(cfg.Audio.HardwareDeviceType.Name()),
			HardwareDeviceName: string(cfg.Audio.HardwareDeviceName),
		},
		Video: &ffstream_grpc.CodecConfig{
			CodecName:          cfg.Video.CodecName,
			AveragingPeriod:    uint64(DurationToGRPC(cfg.Video.AveragingPeriod)),
			AverageBitRate:     cfg.Video.AverageBitRate,
			CustomOptions:      CustomOptionsToGRPC(cfg.Video.CustomOptions),
			HardwareDeviceType: string(cfg.Video.HardwareDeviceType.Name()),
			HardwareDeviceName: string(cfg.Video.HardwareDeviceName),
		},
	}
}
