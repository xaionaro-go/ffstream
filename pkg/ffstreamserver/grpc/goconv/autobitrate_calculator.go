package goconv

import (
	"fmt"
	"time"

	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avpipeline_grpc "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
)

func AutoBitRateCalculatorFromGRPC(
	in *avpipeline_grpc.AutoBitrateCalculator,
) (streammuxtypes.AutoBitRateCalculator, error) {
	switch calculator := in.GetAutoBitrateCalculator().(type) {
	case nil:
		return nil, nil
	case *avpipeline_grpc.AutoBitrateCalculator_Thresholds:
		return &streammuxtypes.AutoBitrateCalculatorThresholds{
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
	case *avpipeline_grpc.AutoBitrateCalculator_ConstantQueueSize:
		return &streammuxtypes.AutoBitrateCalculatorConstantQueueSize{
			QueueOptimal:  time.Duration(calculator.ConstantQueueSize.GetQueueOptimalMS()) * time.Millisecond,
			MovingAverage: MovingAverageFromGRPC(calculator.ConstantQueueSize.GetMovingAverage()),
		}, nil
	default:
		return nil, fmt.Errorf("unknown AutoBitRateCalculator type: %T", calculator)
	}
}
func AutoBitRateCalculatorToGRPC(
	in streammuxtypes.AutoBitRateCalculator,
) (*avpipeline_grpc.AutoBitrateCalculator, error) {
	if in == nil {
		return nil, nil
	}

	switch c := in.(type) {
	case *streammuxtypes.AutoBitrateCalculatorThresholds:
		return &avpipeline_grpc.AutoBitrateCalculator{
			AutoBitrateCalculator: &avpipeline_grpc.AutoBitrateCalculator_Thresholds{
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
	case *streammuxtypes.AutoBitrateCalculatorConstantQueueSize:
		return &avpipeline_grpc.AutoBitrateCalculator{
			AutoBitrateCalculator: &avpipeline_grpc.AutoBitrateCalculator_ConstantQueueSize{
				ConstantQueueSize: &avpipeline_grpc.AutoBitrateCalculatorConstantQueueSize{
					QueueOptimalMS: uint64(c.QueueOptimal / time.Millisecond),
					MovingAverage:  MovingAverageToGRPC(c.MovingAverage),
				},
			},
		}, nil
	default:
		return nil, fmt.Errorf("unknown AutoBitRateCalculator type: %T", in)
	}
}
