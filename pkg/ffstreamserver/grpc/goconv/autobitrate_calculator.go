package goconv

import (
	"fmt"
	"time"

	"github.com/xaionaro-go/avpipeline/preset/streammux"
	"github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avpipeline_grpc "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func AutoBitRateCalculatorFromGRPC(
	in *ffstream_grpc.AutoBitRateCalculator,
) (types.AutoBitRateCalculator, error) {
	switch calculator := in.GetCalculator().(type) {
	case nil:
		return nil, nil
	case *ffstream_grpc.AutoBitRateCalculator_Thresholds:
		return &streammux.AutoBitrateCalculatorThresholds{
			OutputExtremelyHighQueueSizeDuration: time.Duration(calculator.Thresholds.GetOutputExtremelyHighQueueSizeDurationMS()) * time.Millisecond,
			OutputVeryHighQueueSizeDuration:      time.Duration(calculator.Thresholds.GetOutputVeryHighQueueSizeDurationMS()) * time.Millisecond,
			OutputHighQueueSizeDuration:          time.Duration(calculator.Thresholds.GetOutputHighQueueSizeDurationMS()) * time.Millisecond,
			OutputLowQueueSizeDuration:           time.Duration(calculator.Thresholds.GetOutputLowQueueSizeDurationMS()) * time.Millisecond,
			OutputVeryLowQueueSizeDuration:       time.Duration(calculator.Thresholds.GetOutputVeryLowQueueSizeDurationMS()) * time.Millisecond,
			IncreaseK:                            calculator.Thresholds.GetIncreaseK(),
			DecreaseK:                            calculator.Thresholds.GetDecreaseK(),
			QuickIncreaseK:                       calculator.Thresholds.GetQuickIncreaseK(),
			QuickDecreaseK:                       calculator.Thresholds.GetQuickDecreaseK(),
			ExtremeDecreaseK:                     calculator.Thresholds.GetExtremeDecreaseK(),
		}, nil
	default:
		return nil, fmt.Errorf("unknown AutoBitRateCalculator type: %T", calculator)
	}
}
func AutoBitRateCalculatorToGRPC(
	in types.AutoBitRateCalculator,
) (*ffstream_grpc.AutoBitRateCalculator, error) {
	if in == nil {
		return nil, nil
	}

	switch c := in.(type) {
	case *streammux.AutoBitrateCalculatorThresholds:
		return &ffstream_grpc.AutoBitRateCalculator{
			Calculator: &ffstream_grpc.AutoBitRateCalculator_Thresholds{
				Thresholds: &avpipeline_grpc.AutoBitRateCalculatorThresholds{
					OutputExtremelyHighQueueSizeDurationMS: uint64(c.OutputExtremelyHighQueueSizeDuration / time.Millisecond),
					OutputVeryHighQueueSizeDurationMS:      uint64(c.OutputVeryHighQueueSizeDuration / time.Millisecond),
					OutputHighQueueSizeDurationMS:          uint64(c.OutputHighQueueSizeDuration / time.Millisecond),
					OutputLowQueueSizeDurationMS:           uint64(c.OutputLowQueueSizeDuration / time.Millisecond),
					OutputVeryLowQueueSizeDurationMS:       uint64(c.OutputVeryLowQueueSizeDuration / time.Millisecond),
					IncreaseK:                              c.IncreaseK,
					DecreaseK:                              c.DecreaseK,
					QuickIncreaseK:                         c.QuickIncreaseK,
					QuickDecreaseK:                         c.QuickDecreaseK,
					ExtremeDecreaseK:                       c.ExtremeDecreaseK,
				},
			},
		}, nil
	default:
		return nil, fmt.Errorf("unknown AutoBitRateCalculator type: %T", in)
	}
}
