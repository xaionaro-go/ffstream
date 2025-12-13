package ffstream

import (
	"slices"
)

type Resource struct {
	URL              string
	Options          []string
	FallbackPriority uint
}

type Resources []Resource

func (s Resources) ByFallbackPriority() []Resources {
	if len(s) == 0 {
		return nil
	}

	groupsByPriority := map[uint]Resources{}
	priorities := make([]uint, 0)
	seen := map[uint]struct{}{}

	for _, r := range s {
		p := r.FallbackPriority
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
