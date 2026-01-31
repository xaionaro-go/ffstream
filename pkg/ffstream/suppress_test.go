package ffstream

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	packetorframefiltercondition "github.com/xaionaro-go/avpipeline/node/filter/packetorframefilter/condition"
	"github.com/xaionaro-go/avpipeline/packet"
	"github.com/xaionaro-go/avpipeline/packetorframe"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

func TestFFStream_ShouldForwardToOutput(t *testing.T) {
	ctx := context.Background()

	s := &FFStream{
		InputsInfo: []Resources{
			{
				{Suppressed: false}, // idx 0
				{Suppressed: true},  // idx 1
			},
		},
	}

	tests := []struct {
		name     string
		priority FallbackPriority
		idx      ResourceIndex
		expected bool
	}{
		{
			name:     "Non-suppressed input",
			priority: 0,
			idx:      0,
			expected: true,
		},
		{
			name:     "Suppressed input",
			priority: 0,
			idx:      1,
			expected: false,
		},
		{
			name:     "Unknown priority",
			priority: 1,
			idx:      0,
			expected: true,
		},
		{
			name:     "Unknown index",
			priority: 0,
			idx:      2,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputPkt := &packet.Input{
				StreamInfo: &packet.StreamInfo{
					PipelineSideData: avptypes.PipelineSideData{tt.priority, tt.idx},
				},
			}

			input := packetorframefiltercondition.Input{
				Input: packetorframe.InputUnion{
					Packet: inputPkt,
				},
			}

			result := s.shouldForwardToOutput(ctx, input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
