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
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/avpipeline/processor"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/secret"
)

type SenderTemplate struct {
	URLTemplate          string
	Options              []avptypes.DictionaryItem
	RetryOutputOnFailure bool
}

func (t *SenderTemplate) GetURL(
	ctx context.Context,
	outputKey streammux.SenderKey,
) string {
	url := t.URLTemplate
	url = strings.ReplaceAll(url, "${a:0:codec}", string(codec.Name(outputKey.AudioCodec).Canonicalize(ctx, true)))
	url = strings.ReplaceAll(url, "${v:0:codec}", string(codec.Name(outputKey.VideoCodec).Canonicalize(ctx, true)))
	url = strings.ReplaceAll(url, "${v:0:width}", fmt.Sprintf("%d", outputKey.Resolution.Width))
	url = strings.ReplaceAll(url, "${v:0:height}", fmt.Sprintf("%d", outputKey.Resolution.Height))
	return url
}

type senderFactory FFStream

var _ streammux.SenderFactory = (*senderFactory)(nil)

func (s *FFStream) asSenderFactory() *senderFactory {
	return (*senderFactory)(s)
}

func (s *senderFactory) asFFStream() *FFStream {
	return (*FFStream)(s)
}

type SendingNode interface {
	streammux.SendingNode
	streammux.SetDropOnCloser
}

func (s *senderFactory) NewSender(
	ctx context.Context,
	outputKey streammux.SenderKey,
) (streammux.SendingNode, streammuxtypes.SenderConfig, error) {
	if len(s.OutputTemplates) != 1 {
		return nil, streammuxtypes.SenderConfig{}, fmt.Errorf("exactly one output template is required, got %d", len(s.OutputTemplates))
	}
	outputTemplate := s.OutputTemplates[0]
	outputURL := outputTemplate.GetURL(ctx, outputKey)
	resCfg := s.asFFStream().StreamMux.AutoBitRateHandler.AutoBitRateConfig.ResolutionsAndBitRates.Find(outputKey.Resolution)
	var sendBufSize uint
	if resCfg == nil {
		if outputKey.Resolution != (codec.Resolution{}) {
			logger.Errorf(ctx, "unable to find bitrate config for resolution %v, using default send buffer size", outputKey.Resolution)
		}
		resCfg = s.asFFStream().StreamMux.AutoBitRateHandler.AutoBitRateConfig.ResolutionsAndBitRates.Best()
	}
	sendBufSize = uint(resCfg.BitrateHigh.ToBps() * 1000 / 1000) // the buffer should be maxed out if we send traffic over 1000ms round-trip latency channel.
	sendBufSize = max(sendBufSize, 10*1024)                      // at least 10KB
	if outputTemplate.RetryOutputOnFailure {
		return s.newOutputWithRetry(ctx, outputTemplate, outputURL, sendBufSize)
	}
	return s.newOutput(ctx, outputTemplate, outputURL, sendBufSize)
}

func (s *senderFactory) newOutput(
	ctx context.Context,
	outputTemplate SenderTemplate,
	outputURL string,
	bufSize uint,
) (SendingNode, streammuxtypes.SenderConfig, error) {
	outputKernel, err := kernel.NewOutputFromURL(ctx, outputURL, secret.New(""), kernel.OutputConfig{
		CustomOptions:  outputTemplate.Options,
		SendBufferSize: bufSize,
	})
	if err != nil {
		return nil, streammuxtypes.SenderConfig{}, fmt.Errorf("unable to create output from URL %q: %w", outputURL, err)
	}

	outputNode := node.NewWithCustomDataFromKernel[streammux.OutputCustomData](ctx, outputKernel, processor.DefaultOptionsOutput()...)
	return nodeSetDropOnCloserWrapper{outputNode}, streammuxtypes.SenderConfig{}, nil
}

func (s *senderFactory) newOutputWithRetry(
	ctx context.Context,
	outputTemplate SenderTemplate,
	outputURL string,
	bufSize uint,
) (SendingNode, streammuxtypes.SenderConfig, error) {
	outputKernel := kernel.NewRetry(
		ctx,
		func(ctx context.Context) (*kernel.Output, error) {
			outputKernel, err := kernel.NewOutputFromURL(ctx, outputURL, secret.New(""), kernel.OutputConfig{
				CustomOptions:  outputTemplate.Options,
				SendBufferSize: bufSize,
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

	retryOutputNode := node.NewWithCustomDataFromKernel[streammux.OutputCustomData](ctx, outputKernel, processor.DefaultOptionsOutput()...)
	return nodeWithRetrySetDropOnCloserWrapper{retryOutputNode}, streammuxtypes.SenderConfig{}, nil
}
