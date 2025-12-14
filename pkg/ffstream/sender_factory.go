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
	URLTemplate                 string
	Options                     []avptypes.DictionaryItem
	RetryOutputTimeoutOnFailure time.Duration
}

func (t *SenderTemplate) GetURL(
	ctx context.Context,
	outputKey streammux.SenderKey,
) string {
	url := t.URLTemplate
	var audioSampleRate, videoWidth, videoHeight string
	if outputKey.AudioSampleRate != 0 {
		audioSampleRate = fmt.Sprintf("%d", outputKey.AudioSampleRate)
	}
	if outputKey.VideoResolution.Width != 0 {
		videoWidth = fmt.Sprintf("%d", outputKey.VideoResolution.Width)
	}
	if outputKey.VideoResolution.Height != 0 {
		videoHeight = fmt.Sprintf("%d", outputKey.VideoResolution.Height)
	}
	url = strings.ReplaceAll(url, "${a:0:codec}", string(codec.Name(outputKey.AudioCodec).Canonicalize(ctx, true)))
	url = strings.ReplaceAll(url, "${a:0:rate}", audioSampleRate)
	url = strings.ReplaceAll(url, "${v:0:codec}", string(codec.Name(outputKey.VideoCodec).Canonicalize(ctx, true)))
	url = strings.ReplaceAll(url, "${v:0:width}", videoWidth)
	url = strings.ReplaceAll(url, "${v:0:height}", videoHeight)
	return url
}

type senderFactory FFStream

var _ streammux.SenderFactory[CustomData] = (*senderFactory)(nil)

func (s *FFStream) asSenderFactory() *senderFactory {
	return (*senderFactory)(s)
}

func (s *senderFactory) asFFStream() *FFStream {
	return (*FFStream)(s)
}

type SendingNodeAbstract interface {
	streammux.SendingNode[CustomData]
	streammux.SetDropOnCloser
}

func (s *senderFactory) NewSender(
	ctx context.Context,
	outputKey streammux.SenderKey,
) (streammux.SendingNode[CustomData], streammuxtypes.SenderConfig, error) {
	if len(s.OutputTemplates) != 1 {
		return nil, streammuxtypes.SenderConfig{}, fmt.Errorf("exactly one output template is required, got %d", len(s.OutputTemplates))
	}
	outputTemplate := s.OutputTemplates[0]
	outputURL := outputTemplate.GetURL(ctx, outputKey)
	resCfg := s.asFFStream().StreamMux.AutoBitRateHandler.AutoBitRateVideoConfig.ResolutionsAndBitRates.Find(outputKey.VideoResolution)
	var sendBufSize uint
	if resCfg == nil {
		if outputKey.VideoResolution != (codec.Resolution{}) {
			logger.Errorf(ctx, "unable to find bitrate config for resolution %v, using default send buffer size", outputKey.VideoResolution)
		}
		resCfg = s.asFFStream().StreamMux.AutoBitRateHandler.AutoBitRateVideoConfig.ResolutionsAndBitRates.Best()
	}
	sendBufSize = uint(resCfg.BitrateHigh.ToBps() * 1000 / 1000) // the buffer should be maxed out if we send traffic over 1000ms round-trip latency channel.
	sendBufSize = max(sendBufSize, 10*1024)                      // at least 10KB
	if outputTemplate.RetryOutputTimeoutOnFailure != 0 {
		return s.newOutputWithRetry(ctx, outputTemplate, outputURL, sendBufSize, outputTemplate.RetryOutputTimeoutOnFailure)
	}
	return s.newOutput(ctx, outputTemplate, outputURL, sendBufSize)
}

func (s *senderFactory) newOutputKernel(
	ctx context.Context,
	outputTemplate SenderTemplate,
	outputURL string,
	bufSize uint,
) (_ret *kernel.Output, _err error) {
	logger.Debugf(ctx, "newOutputKernel(ctx, %#+v, %q, %d)", outputTemplate, outputURL, bufSize)
	defer func() {
		logger.Debugf(ctx, "/newOutputKernel(ctx, %#+v, %q, %d): %#+v, %v", outputTemplate, outputURL, bufSize, _ret, _err)
	}()
	waitForStreams := kernel.OutputConfigWaitForOutputStreams{}
	switch s.StreamMux.MuxMode {
	case streammuxtypes.UndefinedMuxMode:
		return nil, fmt.Errorf("undefined mux mode")
	case streammuxtypes.MuxModeForbid:
	case streammuxtypes.MuxModeSameOutputSameTracks:
	case streammuxtypes.MuxModeSameOutputDifferentTracks:
	case streammuxtypes.MuxModeDifferentOutputsSameTracks:
	case streammuxtypes.MuxModeDifferentOutputsSameTracksSplitAV:
		waitForStreams.VideoBeforeAudio = ptr(false)
	default:
		return nil, fmt.Errorf("unknown mux mode: %q", s.StreamMux.MuxMode)
	}
	outputKernel, err := kernel.NewOutputFromURL(ctx, outputURL, secret.New(""), kernel.OutputConfig{
		CustomOptions:        outputTemplate.Options,
		SendBufferSize:       bufSize,
		WaitForOutputStreams: &waitForStreams,
	})
	if err != nil {
		return nil, fmt.Errorf("unable to create output from URL %q: %w", outputURL, err)
	}

	outputKernel.Filter = s.OutputQualityMeasurer
	return outputKernel, nil
}

func (s *senderFactory) newOutput(
	ctx context.Context,
	outputTemplate SenderTemplate,
	outputURL string,
	bufSize uint,
) (_ret0 SendingNodeAbstract, _ret1 streammuxtypes.SenderConfig, _err error) {
	logger.Debugf(ctx, "newOutput(ctx, %#+v, %q, %d)", outputTemplate, outputURL, bufSize)
	defer func() {
		logger.Debugf(ctx, "/newOutput(ctx, %#+v, %q, %d): %#+v, %#+v, %v", outputTemplate, outputURL, bufSize, _ret0, _ret1, _err)
	}()

	outputKernel, err := s.newOutputKernel(ctx, outputTemplate, outputURL, bufSize)
	if err != nil {
		return nil, streammuxtypes.SenderConfig{}, fmt.Errorf("unable to create output kernel: %w", err)
	}
	outputNode := node.NewWithCustomDataFromKernel[streammux.OutputCustomData[CustomData]](ctx, outputKernel, processor.DefaultOptionsOutput()...)
	return nodeSetDropOnCloserWrapper{outputNode}, streammuxtypes.SenderConfig{}, nil
}

func (s *senderFactory) newOutputWithRetry(
	ctx context.Context,
	outputTemplate SenderTemplate,
	outputURL string,
	bufSize uint,
	retryTimeout time.Duration,
) (_ret0 SendingNodeAbstract, _ret1 streammuxtypes.SenderConfig, _err error) {
	logger.Debugf(ctx, "newOutputWithRetry(ctx, %#+v, %q, %d, %v)", outputTemplate, outputURL, bufSize, retryTimeout)
	defer func() {
		logger.Debugf(ctx, "/newOutputWithRetry(ctx, %#+v, %q, %d, %v): %#+v, %#+v, %v", outputTemplate, outputURL, bufSize, retryTimeout, _ret0, _ret1, _err)
	}()
	var errorsStartedAt time.Time
	outputKernel := kernel.NewRetryable(
		ctx,
		func(ctx context.Context) (_ret *kernel.Output, _err error) {
			outputKernel, err := s.newOutputKernel(ctx, outputTemplate, outputURL, bufSize)
			if err != nil {
				return nil, fmt.Errorf("(retryable-node:) unable to create output kernel: %w", err)
			}
			return outputKernel, nil
		},
		func(ctx context.Context, k *kernel.Output, err error) error {
			now := time.Now()
			if errorsStartedAt.IsZero() {
				errorsStartedAt = now
			}
			if now.Sub(errorsStartedAt) > retryTimeout {
				logger.Errorf(ctx, "retry timeout exceeded (%v), not retrying output anymore", retryTimeout)
				return err
			}
			logger.Debugf(ctx, "connection ended: %v", err)
			time.Sleep(100 * time.Millisecond)
			return kernel.ErrRetry{Err: err}
		},
		kernel.RetryableOptionOnKernelOpen[*kernel.Output](func(ctx context.Context, k *kernel.Output) error {
			errorsStartedAt = time.Time{}
			return nil
		}),
	)

	retryOutputNode := node.NewWithCustomDataFromKernel[streammux.OutputCustomData[CustomData]](
		ctx, outputKernel, processor.DefaultOptionsOutput()...,
	)
	return nodeWithRetrySetDropOnCloserWrapper{retryOutputNode}, streammuxtypes.SenderConfig{}, nil
}
