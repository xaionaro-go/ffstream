package ffstream

import (
	"context"
	"fmt"
	"strings"

	"github.com/xaionaro-go/avpipeline/codec"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/node"
	streammux "github.com/xaionaro-go/avpipeline/preset/streammux"
	"github.com/xaionaro-go/avpipeline/processor"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/secret"
)

type OutputTemplate struct {
	URLTemplate       string
	Options           []avptypes.DictionaryItem
	AutoBitRateConfig *streammux.AutoBitRateConfig
}

func (t *OutputTemplate) GetURL(
	ctx context.Context,
	outputKey streammux.OutputKey,
) string {
	url := t.URLTemplate
	url = strings.ReplaceAll(url, "${a:0:codec}", string(codec.Name(outputKey.AudioCodec).Canonicalize(ctx, true)))
	url = strings.ReplaceAll(url, "${v:0:codec}", string(codec.Name(outputKey.VideoCodec).Canonicalize(ctx, true)))
	url = strings.ReplaceAll(url, "${v:0:width}", fmt.Sprintf("%d", outputKey.Resolution.Width))
	url = strings.ReplaceAll(url, "${v:0:height}", fmt.Sprintf("%d", outputKey.Resolution.Height))
	return url
}

type outputFactory FFStream

var _ streammux.OutputFactory = (*outputFactory)(nil)

func (s *FFStream) asOutputFactory() *outputFactory {
	return (*outputFactory)(s)
}

func (s *outputFactory) NewOutput(
	ctx context.Context,
	outputKey streammux.OutputKey,
) (node.Abstract, streammux.OutputConfig, error) {
	if len(s.OutputTemplates) != 1 {
		return nil, streammux.OutputConfig{}, fmt.Errorf("exactly one output template is required, got %d", len(s.OutputTemplates))
	}
	outputTemplate := s.OutputTemplates[0]
	outputURL := outputTemplate.GetURL(ctx, outputKey)
	outputKernel, err := kernel.NewOutputFromURL(ctx, outputURL, secret.New(""), kernel.OutputConfig{
		CustomOptions: outputTemplate.Options,
	})
	if err != nil {
		return nil, streammux.OutputConfig{}, fmt.Errorf("unable to create output from URL %q: %w", outputURL, err)
	}

	outputNode := node.NewFromKernel(ctx, outputKernel, processor.DefaultOptionsOutput()...)
	return outputNode, streammux.OutputConfig{
		AutoBitrate: outputTemplate.AutoBitRateConfig,
	}, nil
}
