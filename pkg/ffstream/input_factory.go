// input_factory.go implements InputFactory to create input chains and manage stream index mapping.

package ffstream

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/asticode/go-astiav"
	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/kernel/android"
	"github.com/xaionaro-go/avpipeline/packetorframe"
	"github.com/xaionaro-go/avpipeline/packetorframe/condition"
	"github.com/xaionaro-go/avpipeline/preset/inputwithfallback"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/secret"
	"github.com/xaionaro-go/xsync"
)

type Input = kernel.ChainOfTwo[
	kernel.Tee[kernel.Abstract],
	*kernel.MapStreamIndices,
]

type InputFactory struct {
	FFStream         *FFStream
	FallbackPriority uint
	Locker           xsync.Mutex

	streamIndexNext int
	streamIndexMap  map[streamIndexKey]int
}

type streamIndexKey struct {
	Source any
	Index  int
}

var (
	_ inputwithfallback.InputFactory[*Input, *DecoderFactory, CustomData] = (*InputFactory)(nil)
	_ kernel.StreamIndexAssigner                                          = (*InputFactory)(nil)
)

func newInputFactory(
	ffstream *FFStream,
	priority uint,
) *InputFactory {
	return &InputFactory{
		FFStream:         ffstream,
		FallbackPriority: priority,
	}
}

func (f *InputFactory) String() string {
	return fmt.Sprintf("ffstream:inputFactory(priority=%d)", f.FallbackPriority)
}

// StreamIndexAssign implements kernel.StreamIndexAssigner.
//
// Each underlying `*kernel.Input` in the Tee typically starts its streams from index 0.
// `kernel.MapStreamIndices` uses this callback to remap those per-input indexes into a
// single output index space, preventing collisions across sources.
func (f *InputFactory) StreamIndexAssign(
	ctx context.Context,
	in packetorframe.InputUnion,
) (_ret []int, _err error) {
	logger.Debugf(ctx, "inputFactory.StreamIndexAssign(priority=%d): %v", f.FallbackPriority, in.GetStreamIndex())
	defer func() {
		logger.Debugf(ctx, "/inputFactory.StreamIndexAssign(priority=%d): %v, %v", f.FallbackPriority, _ret, _err)
	}()
	return xsync.DoA2R2(ctx, &f.Locker, f.streamIndexAssignLocked, ctx, in)
}

func (f *InputFactory) streamIndexAssignLocked(
	ctx context.Context,
	in packetorframe.InputUnion,
) ([]int, error) {
	streamIdx := in.GetStreamIndex()
	src := in.GetSource()
	if src == nil {
		return nil, fmt.Errorf("StreamIndexAssign: input source is nil")
	}

	srcIdx, ok := avptypes.PipelineSideDataLatest[ResourceIndex](in.GetPipelineSideData())
	if !ok {
		return nil, fmt.Errorf("StreamIndexAssign: no ResourceIndex in input source pipeline side data")
	}

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

	f.FFStream.onStreamMapped(ctx, f.FallbackPriority, srcIdx, in.GetMediaType(), out)

	return []int{out}, nil
}

func (f *InputFactory) GetResources(
	ctx context.Context,
) (_ret Resources, _err error) {
	logger.Debugf(ctx, "inputFactory.GetResources(%d)", f.FallbackPriority)
	defer func() {
		logger.Debugf(ctx, "/inputFactory.GetResources(%d): %v, %v", f.FallbackPriority, _ret, _err)
	}()

	if f.FFStream == nil {
		return nil, fmt.Errorf("FFStream is nil")
	}
	if int(f.FallbackPriority) >= len(f.FFStream.InputsInfo) {
		return nil, fmt.Errorf("priority %d is out of range (inputs=%d)", f.FallbackPriority, len(f.FFStream.InputsInfo))
	}

	return f.FFStream.InputsInfo[f.FallbackPriority], nil
}

// ResourceIndex is used as side data key to indicate which resource index
// the packet or frame came from within a specific fallback priority.
type ResourceIndex int

// FallbackPriority is used as side data key to indicate the fallback priority
// level.
type FallbackPriority uint

func (f *InputFactory) NewInput(
	ctx context.Context,
	_ *inputwithfallback.InputChain[*Input, *DecoderFactory, CustomData],
) (_ret *Input, _err error) {
	logger.Debugf(ctx, "inputFactory.NewInput(priority=%d)", f.FallbackPriority)
	defer func() {
		logger.Debugf(ctx, "/inputFactory.NewInput(priority=%d): %v, %v", f.FallbackPriority, _ret, _err)
	}()

	resources, err := f.GetResources(ctx)
	if err != nil {
		return nil, err
	}
	if len(resources) == 0 {
		return nil, fmt.Errorf("no input resources configured for priority %d", f.FallbackPriority)
	}
	logger.Debugf(ctx, "inputFactory.NewInput(priority=%d): %d resources", f.FallbackPriority, len(resources))

	if hasMicrophoneInputs(resources) && hasNonMicrophoneInputs(resources) {
		return nil, fmt.Errorf("android_microphone inputs cannot be mixed with non-microphone inputs in the same fallback priority")
	}

	var inputs kernel.Tee[kernel.Abstract]
	defer func() {
		if _err != nil {
			for _, in := range inputs {
				_ = in.Close(ctx)
			}
		}
	}()
	for idx, res := range resources {
		cfg := kernel.InputConfig{
			CustomOptions: res.CustomOptions,
		}
		for _, opt := range res.CustomOptions {
			switch opt.Key {
			case "force_start_pts":
				ptsStr := opt.Value
				if ptsStr == "keep" {
					cfg.ForceStartPTS = ptr(avptypes.PTSKeep)
					continue
				}
				pts, err := strconv.ParseInt(ptsStr, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("unable to parse force_start_pts %q: %v", ptsStr, err)
				}
				cfg.ForceStartPTS = ptr(pts)
			case "force_start_dts":
				dtsStr := opt.Value
				if dtsStr == "keep" {
					cfg.ForceStartDTS = ptr(avptypes.PTSKeep)
					continue
				}
				dts, err := strconv.ParseInt(dtsStr, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("unable to parse force_start_dts %q: %v", dtsStr, err)
				}
				cfg.ForceStartDTS = ptr(dts)
			}
		}

		inputKernel, err := f.newInputKernel(ctx, idx, res, cfg)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, inputKernel)
	}

	f.Locker.Do(ctx, func() {
		f.streamIndexNext = 1
		f.streamIndexMap = make(map[streamIndexKey]int)
	})

	return kernel.NewChainOfTwo(
		inputs,
		kernel.NewMapStreamIndices(ctx, f),
	), nil
}

func (f *InputFactory) newInputKernel(
	ctx context.Context,
	resourceIdx int,
	res Resource,
	cfg kernel.InputConfig,
) (kernel.Abstract, error) {
	formatName := inputFormatFromResource(res)
	if formatName == android.MicrophoneInputFormat {
		return f.newMicrophoneInput(ctx, resourceIdx, cfg)
	}
	return f.newURLInput(ctx, resourceIdx, res, cfg)
}

func (f *InputFactory) newURLInput(
	ctx context.Context,
	resourceIdx int,
	res Resource,
	cfg kernel.InputConfig,
) (kernel.Abstract, error) {
	in, err := kernel.NewInputFromURL(ctx, res.URL, secret.New(""), cfg)
	if err != nil {
		return nil, fmt.Errorf("unable to create input from URL %q: %w", res.URL, err)
	}
	in.PipelineSideData = append(
		in.PipelineSideData,
		FallbackPriority(f.FallbackPriority),
		ResourceIndex(resourceIdx),
	)
	return in, nil
}

func (f *InputFactory) newMicrophoneInput(
	ctx context.Context,
	resourceIdx int,
	cfg kernel.InputConfig,
) (kernel.Abstract, error) {
	micCfg, err := parseMicrophoneConfig(cfg.CustomOptions)
	if err != nil {
		return nil, err
	}
	mic, err := android.NewMicrophone(ctx, micCfg)
	if err != nil {
		return nil, fmt.Errorf("unable to create android microphone: %w", err)
	}

	kernelFilter := kernel.NewFilter(condition.Function(func(_ context.Context, in packetorframe.InputUnion) bool {
		if v, ok := avptypes.PipelineSideDataLatest[FallbackPriority](in.GetPipelineSideData()); !ok || v != FallbackPriority(f.FallbackPriority) {
			in.AddPipelineSideData(FallbackPriority(f.FallbackPriority))
		}
		if v, ok := avptypes.PipelineSideDataLatest[ResourceIndex](in.GetPipelineSideData()); !ok || v != ResourceIndex(resourceIdx) {
			in.AddPipelineSideData(ResourceIndex(resourceIdx))
		}
		return true
	}))

	return kernel.NewChainOfTwo(
		mic,
		kernelFilter,
	), nil
}

func parseMicrophoneConfig(opts avptypes.DictionaryItems) (android.MicrophoneConfig, error) {
	var cfg android.MicrophoneConfig
	for _, opt := range opts {
		value := strings.TrimSpace(opt.Value)
		switch opt.Key {
		case "device_name":
			cfg.DeviceName = value
		case "library_path":
			cfg.LibraryPath = value
		case "sample_rate":
			if value == "" {
				continue
			}
			rate, err := strconv.Atoi(value)
			if err != nil {
				return android.MicrophoneConfig{}, fmt.Errorf("unable to parse sample_rate %q: %v", opt.Value, err)
			}
			cfg.SampleRate = rate
		case "channels":
			if value == "" {
				continue
			}
			ch, err := strconv.Atoi(value)
			if err != nil {
				return android.MicrophoneConfig{}, fmt.Errorf("unable to parse channels %q: %v", opt.Value, err)
			}
			cfg.Channels = ch
		case "frame_samples":
			if value == "" {
				continue
			}
			fs, err := strconv.Atoi(value)
			if err != nil {
				return android.MicrophoneConfig{}, fmt.Errorf("unable to parse frame_samples %q: %v", opt.Value, err)
			}
			cfg.FrameSamples = fs
		case "buffer_samples":
			if value == "" {
				continue
			}
			bs, err := strconv.Atoi(value)
			if err != nil {
				return android.MicrophoneConfig{}, fmt.Errorf("unable to parse buffer_samples %q: %v", opt.Value, err)
			}
			cfg.BufferSamples = bs
		case "poll_interval":
			if value == "" {
				continue
			}
			dur, err := time.ParseDuration(value)
			if err != nil {
				return android.MicrophoneConfig{}, fmt.Errorf("unable to parse poll_interval %q: %v", opt.Value, err)
			}
			cfg.PollInterval = dur
		case "sample_format":
			if value == "" {
				continue
			}
			sampleFmt, err := parseSampleFormat(value)
			if err != nil {
				return android.MicrophoneConfig{}, fmt.Errorf("unable to parse sample_format %q: %v", opt.Value, err)
			}
			cfg.SampleFormat = sampleFmt
		}
	}
	return cfg, nil
}

func parseSampleFormat(s string) (astiav.SampleFormat, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "u8":
		return astiav.SampleFormatU8, nil
	case "s16":
		return astiav.SampleFormatS16, nil
	}
	return astiav.SampleFormatNone, fmt.Errorf("unsupported sample format '%s' (supported: u8, s16)", s)
}

func inputFormatFromResource(res Resource) string {
	if v := res.CustomOptions.GetFirst("f"); v != nil {
		return strings.TrimSpace(*v)
	}
	return ""
}

func hasMicrophoneInputs(resources Resources) bool {
	for _, res := range resources {
		if inputFormatFromResource(res) == android.MicrophoneInputFormat {
			return true
		}
	}
	return false
}

func hasNonMicrophoneInputs(resources Resources) bool {
	for _, res := range resources {
		if inputFormatFromResource(res) != android.MicrophoneInputFormat {
			return true
		}
	}
	return false
}

func (f *InputFactory) NewDecoderFactory(
	ctx context.Context,
	_ *inputwithfallback.InputChain[*Input, *DecoderFactory, CustomData],
) (_ret *DecoderFactory, _err error) {
	logger.Debugf(ctx, "inputFactory.NewDecoderFactory(priority=%d)", f.FallbackPriority)
	defer func() {
		logger.Debugf(ctx, "/inputFactory.NewDecoderFactory(priority=%d): %v, %v", f.FallbackPriority, _ret, _err)
	}()

	resources, err := f.GetResources(ctx)
	if err != nil {
		return nil, err
	}
	if len(resources) > 0 {
		allMicrophone := true
		for _, res := range resources {
			if inputFormatFromResource(res) != android.MicrophoneInputFormat {
				allMicrophone = false
				break
			}
		}
		if allMicrophone {
			return nil, nil
		}
	}

	return f.newDecoderFactory(ctx), nil
}
