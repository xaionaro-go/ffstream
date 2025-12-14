package ffstream

import (
	"context"
	"fmt"

	"strings"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/codec"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/packetorframe"
	"github.com/xaionaro-go/avpipeline/preset/inputwithfallback"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/avpipeline/urltools"
	"github.com/xaionaro-go/secret"
	"github.com/xaionaro-go/xsync"
)

type Input = kernel.ChainOfTwo[kernel.Tee[*kernel.Input], *kernel.MapStreamIndices]

type inputFactory struct {
	FFStream         *FFStream
	FallbackPriority uint
	Locker           xsync.Mutex

	streamIndexNext int
	streamIndexMap  map[streamIndexKey]int
	sourceIndex     map[packetorframe.AbstractSource]int
}

type streamIndexKey struct {
	Source any
	Index  int
}

var _ inputwithfallback.InputFactory[*Input, *codec.NaiveDecoderFactory] = (*inputFactory)(nil)
var _ kernel.StreamIndexAssigner = (*inputFactory)(nil)

func newInputFactory(
	ffstream *FFStream,
	priority uint,
) *inputFactory {
	return &inputFactory{
		FFStream:         ffstream,
		FallbackPriority: priority,
		sourceIndex:      make(map[packetorframe.AbstractSource]int),
	}
}

func (f *inputFactory) String() string {
	return fmt.Sprintf("ffstream:inputFactory(priority=%d)", f.FallbackPriority)
}

// StreamIndexAssign implements kernel.StreamIndexAssigner.
//
// Each underlying `*kernel.Input` in the Tee typically starts its streams from index 0.
// `kernel.MapStreamIndices` uses this callback to remap those per-input indexes into a
// single output index space, preventing collisions across sources.
func (f *inputFactory) StreamIndexAssign(
	ctx context.Context,
	in packetorframe.InputUnion,
) (_ret []int, _err error) {
	logger.Debugf(ctx, "inputFactory.StreamIndexAssign(priority=%d): %v", f.FallbackPriority, in.GetStreamIndex())
	defer func() {
		logger.Debugf(ctx, "/inputFactory.StreamIndexAssign(priority=%d): %v, %v", f.FallbackPriority, _ret, _err)
	}()
	return xsync.DoA2R2(ctx, &f.Locker, f.streamIndexAssignLocked, ctx, in)
}

func (f *inputFactory) streamIndexAssignLocked(
	_ context.Context,
	in packetorframe.InputUnion,
) ([]int, error) {
	streamIdx := in.GetStreamIndex()
	src := in.GetSource()
	if src == nil {
		return nil, fmt.Errorf("StreamIndexAssign: input source is nil")
	}

	srcIdx := f.sourceIndex[src]
	if srcIdx == 0 && streamIdx == 0 {
		// there are protocols where the order of streams is important,
		// so we are doing our best to make sure we won't break that,
		// by keeping 0ths stream of the first input as 0.
		return []int{0}, nil
	}

	key := streamIndexKey{Source: src, Index: streamIdx}
	if out, ok := f.streamIndexMap[key]; ok {
		return []int{out}, nil
	}

	out := f.streamIndexNext
	f.streamIndexNext++
	f.streamIndexMap[key] = out
	return []int{out}, nil
}

func (f *inputFactory) NewInput(
	ctx context.Context,
) (_ret *Input, _err error) {
	logger.Debugf(ctx, "inputFactory.NewInput(priority=%d)", f.FallbackPriority)
	defer func() {
		logger.Debugf(ctx, "/inputFactory.NewInput(priority=%d): %v, %v", f.FallbackPriority, _ret, _err)
	}()

	if f.FFStream == nil {
		return nil, fmt.Errorf("FFStream is nil")
	}
	if int(f.FallbackPriority) >= len(f.FFStream.InputsInfo) {
		return nil, fmt.Errorf("priority %d is out of range (inputs=%d)", f.FallbackPriority, len(f.FFStream.InputsInfo))
	}

	resources := f.FFStream.InputsInfo[f.FallbackPriority]
	logger.Debugf(ctx, "inputFactory.NewInput(priority=%d): %d resources", f.FallbackPriority, len(resources))

	var inputs kernel.Tee[*kernel.Input]
	defer func() {
		if _err != nil {
			for _, in := range inputs {
				_ = in.Close(ctx)
			}
		}
	}()
	for _, res := range resources {
		in, err := kernel.NewInputFromURL(ctx, res.URL, secret.New(""), kernel.InputConfig{
			ForceRealTime: urltools.IsFileURL(res.URL),
			CustomOptions: convertUnknownOptionsToAVPCustomOptions(res.Options),
		})
		if err != nil {
			return nil, fmt.Errorf("unable to create input from URL %q: %w", res.URL, err)
		}
		inputs = append(inputs, in)
	}

	f.Locker.Do(ctx, func() {
		f.streamIndexNext = 1
		f.streamIndexMap = make(map[streamIndexKey]int)
		for k := range f.sourceIndex {
			delete(f.sourceIndex, k)
		}
		for idx, in := range inputs {
			f.sourceIndex[packetorframe.AbstractSource(in)] = idx
		}
	})

	return kernel.NewChainOfTwo(inputs, kernel.NewMapStreamIndices(ctx, f)), nil
}

func (f *inputFactory) NewDecoderFactory(
	ctx context.Context,
) (_ret *codec.NaiveDecoderFactory, _err error) {
	logger.Debugf(ctx, "inputFactory.NewDecoderFactory(priority=%d)", f.FallbackPriority)
	defer func() {
		logger.Debugf(ctx, "/inputFactory.NewDecoderFactory(priority=%d): %v, %v", f.FallbackPriority, _ret, _err)
	}()
	return codec.NewNaiveDecoderFactory(ctx, nil), nil
}

func convertUnknownOptionsToAVPCustomOptions(
	unknownOpts []string,
) avptypes.DictionaryItems {
	var result avptypes.DictionaryItems

	for idx := 0; idx < len(unknownOpts)-1; idx += 2 {
		arg := unknownOpts[idx]

		opt := strings.TrimPrefix(arg, "-")
		value := unknownOpts[idx+1]

		result = append(result, avptypes.DictionaryItem{
			Key:   opt,
			Value: value,
		})
	}

	return result
}
