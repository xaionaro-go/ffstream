// decoder_factory.go implements a DecoderFactory that wraps the naive decoder and adds audio normalization support.

package ffstream

import (
	"context"
	"errors"
	"fmt"

	"github.com/asticode/go-astiav"
	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/codec"
	codectypes "github.com/xaionaro-go/avpipeline/codec/types"
	"github.com/xaionaro-go/avpipeline/frame/filter/audionormalize"
	"github.com/xaionaro-go/avpipeline/packet"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

type DecoderFactory struct {
	*codec.NaiveDecoderFactory
	*InputFactory
	AudioNormalize map[int]*audionormalize.AudioNormalize
}

var _ codec.DecoderFactory = (*DecoderFactory)(nil)

func (f *InputFactory) newDecoderFactory(
	ctx context.Context,
) *DecoderFactory {
	return &DecoderFactory{
		NaiveDecoderFactory: codec.NewNaiveDecoderFactory(ctx, nil),
		InputFactory:        f,
		AudioNormalize:      make(map[int]*audionormalize.AudioNormalize),
	}
}

func (f *DecoderFactory) String() string {
	return f.NaiveDecoderFactory.String()
}

func (f *DecoderFactory) NewDecoder(
	ctx context.Context,
	source packet.Source,
	stream *astiav.Stream,
	pipelineSideData avptypes.PipelineSideData,
	opts ...codec.Option,
) (_ret *codec.Decoder, _err error) {
	logger.Debugf(ctx, "NewDecoder: stream_index:%d", stream.Index())
	defer func() {
		logger.Debugf(ctx, "/NewDecoder: stream_index:%d: %v, %v", stream.Index(), _ret, _err)
	}()
	resourceIndex, ok := avptypes.PipelineSideDataLatest[ResourceIndex](pipelineSideData)
	if ok {
		r := f.FFStream.InputsInfo[f.FallbackPriority][int(resourceIndex)]
		opts = append(opts, codectypes.OptionOverrideCustomOptions(r.CustomOptions))
		if stream.CodecParameters().MediaType() == astiav.MediaTypeVideo {
			opts = append(opts, codectypes.OptionOverrideHardwareDeviceType(r.CodecHWAccel))
		}
	} else {
		logger.Errorf(ctx, "stream_index:%d: no ResourceIndex in PipelineSideData", stream.Index())
	}
	return f.NaiveDecoderFactory.NewDecoder(
		ctx,
		source,
		stream,
		pipelineSideData,
		opts...,
	)
}

func (f *DecoderFactory) Reset(
	ctx context.Context,
) (_ret error) {
	var errs []error
	if f.AudioNormalize != nil {
		for inputNum, an := range f.AudioNormalize {
			if err := an.Reset(ctx); err != nil {
				errs = append(errs, fmt.Errorf("audio normalize reset error (input num %d): %w", inputNum, err))
			}
		}
	}
	if err := f.NaiveDecoderFactory.Reset(ctx); err != nil {
		errs = append(errs, fmt.Errorf("naive decoder factory reset error: %w", err))
	}
	return errors.Join(errs...)
}
