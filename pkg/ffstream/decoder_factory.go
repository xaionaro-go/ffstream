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

// lookupResource returns a copy of the Resource at (f.FallbackPriority, idx)
// while holding FFStream.locker, or reports ok=false if the indices are out
// of range. The copy isolates the caller from concurrent mutations of the
// slice after the lock is released.
func (f *DecoderFactory) lookupResource(idx ResourceIndex) (Resource, bool) {
	f.FFStream.locker.Lock()
	defer f.FFStream.locker.Unlock()
	if int(f.FallbackPriority) >= len(f.FFStream.InputsInfo) {
		return Resource{}, false
	}
	resources := f.FFStream.InputsInfo[f.FallbackPriority]
	if int(idx) < 0 || int(idx) >= len(resources) {
		return Resource{}, false
	}
	return resources[idx], true
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
		// Read the Resource under FFStream.locker, because InputsInfo is mutated
		// by AddInput/SetSuppressed/SetInputCustomOption from other goroutines.
		r, ok := f.lookupResource(resourceIndex)
		if ok {
			customOptions := r.CustomOptions
			if stream.CodecParameters().MediaType() == astiav.MediaTypeVideo &&
				r.CodecHWAccel == avptypes.HardwareDeviceTypeMediaCodec {
				// Augment the per-Resource customOptions with the MediaCodec
				// surface-passthrough hints. These flow through codec.go into
				// av_hwdevice_ctx_create, which honours create_window=1 by
				// allocating a persistent ANativeWindow on the hwdevice (see
				// libavutil/hwcontext_mediacodec.c mc_device_init). Without
				// this, the decoder falls back to buffer-mode output and the
				// downstream encoder cannot reuse the surface.
				// Use append-and-deduplicate (last-wins via Deduplicate) so
				// any caller-supplied keys are preserved over our defaults.
				augmented := make(avptypes.DictionaryItems, 0, len(customOptions)+2)
				augmented = append(augmented, avptypes.DictionaryItem{Key: "pixel_format", Value: "mediacodec"})
				augmented = append(augmented, avptypes.DictionaryItem{Key: "create_window", Value: "1"})
				augmented = append(augmented, customOptions...)
				customOptions = augmented.Deduplicate()
			}
			opts = append(opts, codectypes.OptionOverrideCustomOptions(customOptions))
			if stream.CodecParameters().MediaType() == astiav.MediaTypeVideo {
				opts = append(opts, codectypes.OptionOverrideHardwareDeviceType(r.CodecHWAccel))
			}
		} else {
			logger.Errorf(ctx, "stream_index:%d: resource (priority=%d, idx=%d) is out of range", stream.Index(), f.FallbackPriority, resourceIndex)
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
