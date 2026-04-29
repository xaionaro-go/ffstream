// input_factory.go implements InputFactory to create input chains and manage stream index mapping.

package ffstream

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/kernel"
	"github.com/xaionaro-go/avpipeline/kernel/extra/android"
	"github.com/xaionaro-go/avpipeline/kernel/v4l2"
	"github.com/xaionaro-go/avpipeline/packetorframe"
	"github.com/xaionaro-go/avpipeline/packetorframe/condition"
	"github.com/xaionaro-go/avpipeline/preset/inputwithfallback"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/secret"
	"github.com/xaionaro-go/xsync"
)

// Input is the per-priority input chain. Inner Tee uses kernel.Abstract
// so the slice can hold both `*kernel.Input` (libav-backed URL inputs)
// and special-cased non-libav kernels — currently
// avpipeline/kernel/extra/android.Microphone — alongside one another.
// This widening is the prerequisite for inputFormatFromResource()-driven
// dispatch in newInputKernel, which routes `f=android_microphone` to
// android.NewMicrophone instead of (libav-only) kernel.NewInputFromURL.
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
	_ inputwithfallback.InputFactoryWithAvailability                      = (*InputFactory)(nil)
	_ kernel.StreamIndexAssigner                                          = (*InputFactory)(nil)
)

func newInputFactory(
	ffstream *FFStream,
	priority uint,
) *InputFactory {
	return &InputFactory{
		FFStream:         ffstream,
		FallbackPriority: priority,
		streamIndexMap:   make(map[streamIndexKey]int),
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

// GetResources returns a deep-copy snapshot of the Resources configured for
// this InputFactory's FallbackPriority, taking
// FFStream.Inputs.InputChainsLocker so that concurrent AddInput /
// RemoveInput writers (which mutate InputsInfo under the same lock) do
// not race the slice header against this read. The returned Resources
// are safe to read without further locking; they do not alias the live
// slice.
//
// Callers that already hold InputChainsLocker MUST call
// GetResourcesLocked instead — the lock is non-reentrant and a nested
// acquire deadlocks.
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
	return xsync.DoR2(ctx, &f.FFStream.Inputs.InputChainsLocker, func() (Resources, error) {
		return f.getResourcesLocked()
	})
}

// GetResourcesLocked is the in-lock variant of GetResources. The caller
// MUST hold FFStream.Inputs.InputChainsLocker. Returns a defensive
// copy so the caller can release the lock before iterating.
func (f *InputFactory) GetResourcesLocked() (Resources, error) {
	if f.FFStream == nil {
		return nil, fmt.Errorf("FFStream is nil")
	}
	return f.getResourcesLocked()
}

func (f *InputFactory) getResourcesLocked() (Resources, error) {
	if int(f.FallbackPriority) >= len(f.FFStream.InputsInfo) {
		return nil, fmt.Errorf("priority %d is out of range (inputs=%d)", f.FallbackPriority, len(f.FFStream.InputsInfo))
	}
	src := f.FFStream.InputsInfo[f.FallbackPriority]
	out := make(Resources, len(src))
	copy(out, src)
	return out, nil
}

// HasResources implements inputwithfallback.InputFactoryWithAvailability.
//
// The avpipeline fallback walk in InputWithFallback.onInputChainError
// consults this method on every chain it scans past id+1 and skips
// chains that report HasResources=false. This lets the walk jump
// directly from the failing chain to the next priority that has
// resources configured, rather than serializing each empty priority
// through the procN switching latch — which is the race that
// produced "another switch is in progress" log spam under
// `-fallback_priority 10` with the camera at priority 0.
//
// HasResources is a live read of InputsInfo[priority] under
// InputChainsLocker; it is not cached, so RemoveInput on the only
// resource at a priority flips the verdict back to false on the next
// fallback walk.
//
// Locking contract (per InputFactoryWithAvailability): the avpipeline
// fallback walk in InputWithFallback.onInputChainError invokes this
// method while it already holds InputChainsLocker. The lock is
// non-reentrant (xsync.Mutex == xsync.RWMutex; Do takes a write lock
// that recursing same-goroutine deadlocks). Therefore HasResources
// MUST read InputsInfo through the *Locked variant only — calling
// the locking GetResources here self-deadlocks the goroutine forever
// (observed: prod ffstream wedged 27+ minutes in xsync.RWMutex.Lock
// at xsync.DoR2 -> InputFactory.GetResources from this call site).
func (f *InputFactory) HasResources(ctx context.Context) bool {
	if f.FFStream == nil {
		logger.Debugf(ctx, "InputFactory.HasResources(priority=%d): FFStream is nil", f.FallbackPriority)
		return false
	}
	resources, err := f.GetResourcesLocked()
	if err != nil {
		// Out-of-range priority is treated as empty (no resource is
		// effectively reachable). This matches the legacy behavior:
		// a chain with no live resource cannot be opened by NewInput
		// and would error immediately if the fallback walk landed on
		// it, so skipping it is always the right call.
		logger.Debugf(ctx, "InputFactory.HasResources(priority=%d): GetResourcesLocked failed: %v", f.FallbackPriority, err)
		return false
	}
	return len(resources) > 0
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
) (_ret kernel.Abstract, _err error) {
	formatName := inputFormatFromResource(res)
	logger.Debugf(ctx, "inputFactory.newInputKernel(priority=%d, resourceIdx=%d): format=%q, url=%q", f.FallbackPriority, resourceIdx, formatName, res.URL)
	defer func() {
		logger.Debugf(ctx, "/inputFactory.newInputKernel(priority=%d, resourceIdx=%d): %v, %v", f.FallbackPriority, resourceIdx, _ret, _err)
	}()
	switch {
	case formatName == android.MicrophoneInputFormat:
		return f.newMicrophoneInput(ctx, resourceIdx, res.URL, cfg)
	case v4l2.IsV4L2Format(formatName) && strings.HasPrefix(res.URL, "name:"):
		var matchIndex *int
		if v := res.CustomOptions.GetFirst("match_index"); v != nil {
			idx, err := strconv.Atoi(strings.TrimSpace(*v))
			if err != nil {
				return nil, fmt.Errorf("unable to parse match_index %q: %w", *v, err)
			}
			matchIndex = &idx
		}
		devicePath, err := v4l2.ResolveDeviceByName(ctx, strings.TrimPrefix(res.URL, "name:"), matchIndex)
		if err != nil {
			return nil, fmt.Errorf("unable to resolve V4L2 device: %w", err)
		}
		res.URL = devicePath
		return f.newURLInput(ctx, resourceIdx, res, cfg)
	default:
		return f.newURLInput(ctx, resourceIdx, res, cfg)
	}
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
	inputURL string,
	cfg kernel.InputConfig,
) (kernel.Abstract, error) {
	micCfg, err := parseMicrophoneConfig(inputURL, cfg.CustomOptions)
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

func parseMicrophoneConfig(inputURL string, opts avptypes.DictionaryItems) (android.MicrophoneConfig, error) {
	var cfg android.MicrophoneConfig
	if inputURL != "" {
		trimmed := strings.TrimSpace(inputURL)
		switch {
		case strings.HasPrefix(trimmed, "name:"):
			cfg.DeviceNamePattern = strings.TrimPrefix(trimmed, "name:")
		default:
			id, err := strconv.ParseInt(trimmed, 10, 32)
			if err != nil {
				return android.MicrophoneConfig{}, fmt.Errorf(
					"invalid microphone input %q: use a numeric device ID or \"name:<pattern>\"",
					inputURL,
				)
			}
			devID := int32(id)
			cfg.DeviceID = &devID
		}
	}
	for _, opt := range opts {
		value := strings.TrimSpace(opt.Value)
		switch opt.Key {
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
		case "disable_sensor_privacy_on_start":
			cfg.DisableSensorPrivacyOnStart = value == "1" || value == "true"
		case "disable_sensor_privacy_on_silence":
			cfg.DisableSensorPrivacyOnSilence = value == "1" || value == "true"
		case "input_preset":
			if value == "" {
				continue
			}
			preset, err := strconv.Atoi(value)
			if err != nil {
				return android.MicrophoneConfig{}, fmt.Errorf("unable to parse input_preset %q: %v", opt.Value, err)
			}
			cfg.InputPreset = android.InputPreset(preset)
		}
	}
	return cfg, nil
}

func inputFormatFromResource(res Resource) string {
	if v := res.CustomOptions.GetFirst("f"); v != nil {
		return strings.TrimSpace(*v)
	}
	return ""
}

func (f *InputFactory) NewDecoderFactory(
	ctx context.Context,
	_ *inputwithfallback.InputChain[*Input, *DecoderFactory, CustomData],
) (_ret *DecoderFactory, _err error) {
	logger.Debugf(ctx, "inputFactory.NewDecoderFactory(priority=%d)", f.FallbackPriority)
	defer func() {
		logger.Debugf(ctx, "/inputFactory.NewDecoderFactory(priority=%d): %v, %v", f.FallbackPriority, _ret, _err)
	}()

	// NewDecoderFactory is invoked from FFStream.AddInput via
	// inputwithfallback.addFactory while InputChainsLocker is already
	// held by this goroutine; re-acquiring it would deadlock the
	// non-reentrant lock. Use GetResourcesLocked to read the resources
	// under the inherited lock.
	resources, err := f.GetResourcesLocked()
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
