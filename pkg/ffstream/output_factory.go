package ffstream

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/codec"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/node"
	streammux "github.com/xaionaro-go/avpipeline/preset/streammux"
	"github.com/xaionaro-go/avpipeline/processor"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/secret"
)

type OutputTemplate struct {
	URLTemplate          string
	Options              []avptypes.DictionaryItem
	RetryOutputOnFailure bool
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
	if outputTemplate.RetryOutputOnFailure {
		return s.newOutputWithRetry(ctx, outputTemplate, outputURL)
	}
	return s.newOutput(ctx, outputTemplate, outputURL)
}

func (s *outputFactory) newOutput(
	ctx context.Context,
	outputTemplate OutputTemplate,
	outputURL string,
) (node.Abstract, streammux.OutputConfig, error) {
	outputKernel, err := kernel.NewOutputFromURL(ctx, outputURL, secret.New(""), kernel.OutputConfig{
		CustomOptions: outputTemplate.Options,
	})
	if err != nil {
		return nil, streammux.OutputConfig{}, fmt.Errorf("unable to create output from URL %q: %w", outputURL, err)
	}

	outputNode := node.NewFromKernel(ctx, outputKernel, processor.DefaultOptionsOutput()...)
	return outputNode, streammux.OutputConfig{}, nil
}

func (s *outputFactory) newOutputWithRetry(
	ctx context.Context,
	outputTemplate OutputTemplate,
	outputURL string,
) (node.Abstract, streammux.OutputConfig, error) {
	outputKernel := kernel.NewRetry(
		ctx,
		func(ctx context.Context) (*kernel.Output, error) {
			outputKernel, err := kernel.NewOutputFromURL(ctx, outputURL, secret.New(""), kernel.OutputConfig{
				CustomOptions: outputTemplate.Options,
			})
			if err != nil {
				return nil, fmt.Errorf("unable to create output from URL %q: %w", outputURL, err)
			}
			return outputKernel, nil
		},
		func(ctx context.Context, k *kernel.Output) error {
			return nil
		},
		func(ctx context.Context, k *kernel.Output, err error) error {
			logger.Debugf(ctx, "connection ended: %v", err)
			time.Sleep(100 * time.Millisecond)
			return kernel.ErrRetry{Err: err}
		},
	)

	retryOutputNode := node.NewFromKernel(ctx, outputKernel, processor.DefaultOptionsOutput()...)
	return retryOutputNode, streammux.OutputConfig{}, nil
}
