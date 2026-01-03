// decoder_factory.go implements a DecoderFactory that wraps the naive decoder and adds audio normalization support.

package ffstream

import (
	"context"
	"errors"
	"fmt"

	"github.com/asticode/go-astiav"
	"github.com/xaionaro-go/avpipeline/codec"
	"github.com/xaionaro-go/avpipeline/frame/filter/audionormalize"
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
	stream *astiav.Stream,
	opts ...codec.Option,
) (*codec.Decoder, error) {
	return f.NaiveDecoderFactory.NewDecoder(ctx, stream, opts...)
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
