package goconv

import (
	"time"

	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
)

func LatenciesToGRPC(
	in *streammuxtypes.Latencies,
) *ffstream_grpc.Latencies {
	if in == nil {
		return nil
	}

	return &ffstream_grpc.Latencies{
		Audio: TrackLatenciesToGRPC(in.Audio),
		Video: TrackLatenciesToGRPC(in.Video),
	}
}

func TrackLatenciesToGRPC(
	in streammuxtypes.TrackLatencies,
) *ffstream_grpc.TrackLatencies {
	return &ffstream_grpc.TrackLatencies{
		PreRecodingU:    uint64(in.PreRecoding.Nanoseconds()),
		RecodingU:       uint64(in.Recoding.Nanoseconds()),
		RecodedPreSendU: uint64(in.RecodedPreSend.Nanoseconds()),
		SendingU:        uint64(in.Sending.Nanoseconds()),
	}
}

func LatenciesFromGRPC(
	in *ffstream_grpc.Latencies,
) *streammuxtypes.Latencies {
	if in == nil {
		return nil
	}

	return &streammuxtypes.Latencies{
		Audio: TrackLatenciesFromGRPC(in.Audio),
		Video: TrackLatenciesFromGRPC(in.Video),
	}
}

func TrackLatenciesFromGRPC(
	in *ffstream_grpc.TrackLatencies,
) streammuxtypes.TrackLatencies {
	if in == nil {
		return streammuxtypes.TrackLatencies{}
	}

	return streammuxtypes.TrackLatencies{
		PreRecoding:    nanosecondsToDuration(int64(in.PreRecodingU)),
		Recoding:       nanosecondsToDuration(int64(in.RecodingU)),
		RecodedPreSend: nanosecondsToDuration(int64(in.RecodedPreSendU)),
		Sending:        nanosecondsToDuration(int64(in.SendingU)),
	}
}

func nanosecondsToDuration(ns int64) time.Duration {
	return time.Nanosecond * time.Duration(ns)
}
