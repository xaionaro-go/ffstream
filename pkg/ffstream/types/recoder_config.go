package types

import (
	"time"

	"github.com/xaionaro-go/avpipeline/codec"
	"github.com/xaionaro-go/avpipeline/types"
)

type CodecConfig struct {
	CodecName          string
	AveragingPeriod    time.Duration
	AverageBitRate     uint64
	CustomOptions      types.DictionaryItems
	HardwareDeviceType codec.HardwareDeviceType
	HardwareDeviceName codec.HardwareDeviceName
}

type RecoderConfig struct {
	Audio CodecConfig
	Video CodecConfig
}
