// counters.go provides conversion functions for node counters between Go and GRPC types.

package goconv

import (
	avpipeline_grpc "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
)

func AddNodeCountersSection(
	a, b *avpipeline_grpc.NodeCountersSection,
) *avpipeline_grpc.NodeCountersSection {
	if a == nil && b == nil {
		return nil
	}
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &avpipeline_grpc.NodeCountersSection{
		Packets: AddNodeCountersSubSection(a.Packets, b.Packets),
		Frames:  AddNodeCountersSubSection(a.Frames, b.Frames),
	}
}

func AddNodeCountersSubSection(
	a, b *avpipeline_grpc.NodeCountersSubSection,
) *avpipeline_grpc.NodeCountersSubSection {
	if a == nil && b == nil {
		return nil
	}
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &avpipeline_grpc.NodeCountersSubSection{
		Unknown: AddNodeCountersItem(a.Unknown, b.Unknown),
		Other:   AddNodeCountersItem(a.Other, b.Other),
		Video:   AddNodeCountersItem(a.Video, b.Video),
		Audio:   AddNodeCountersItem(a.Audio, b.Audio),
	}
}

func AddNodeCountersItem(a, b *avpipeline_grpc.NodeCountersItem) *avpipeline_grpc.NodeCountersItem {
	if a == nil && b == nil {
		return nil
	}
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &avpipeline_grpc.NodeCountersItem{
		Count: a.Count + b.Count,
		Bytes: a.Bytes + b.Bytes,
	}
}
