// resource.go defines Resource (URL + config) and logic for handling fallback priorities.

package ffstream

import (
	"context"
	"slices"
	"strconv"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/kernel"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

type Resource struct {
	URL                     string
	CodecHWAccel            avptypes.HardwareDeviceType
	SyncUsingReferenceAudio *int
	Suppressed              bool
	kernel.InputConfig
}

func (r Resource) GetFallbackPriority(
	ctx context.Context,
) uint {
	for _, item := range r.CustomOptions {
		if item.Key == "fallback_priority" {
			i, err := strconv.ParseUint(item.Value, 10, 0)
			if err != nil {
				logger.Errorf(ctx, "unable to parse fallback priority %q: %v", item.Value, err)
				continue
			}
			return uint(i)
		}
	}
	return 0
}

type Resources []Resource

// Clone returns a deep copy of the Resources slice. Each Resource's
// CustomOptions slice is cloned so that callers can safely read the returned
// value without synchronising with concurrent mutations of the live
// InputsInfo slice (AddInput / SetSuppressed / SetInputCustomOption).
func (s Resources) Clone() Resources {
	if s == nil {
		return nil
	}
	out := make(Resources, len(s))
	for i, r := range s {
		out[i] = r
		out[i].CustomOptions = slices.Clone(r.CustomOptions)
	}
	return out
}

func (s Resources) ByFallbackPriority(
	ctx context.Context,
) []Resources {
	if len(s) == 0 {
		return nil
	}

	groupsByPriority := map[uint]Resources{}
	priorities := make([]uint, 0)
	seen := map[uint]struct{}{}

	for _, r := range s {
		p := r.GetFallbackPriority(ctx)
		groupsByPriority[p] = append(groupsByPriority[p], r)
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			priorities = append(priorities, p)
		}
	}

	// Sort by ascending priority (0 is the best/default).
	slices.Sort(priorities)

	result := make([]Resources, 0, len(priorities))
	for _, p := range priorities {
		result = append(result, groupsByPriority[p])
	}

	return result
}
