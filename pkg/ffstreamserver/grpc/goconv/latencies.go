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
		PreTranscodingU:    uint64(in.PreTranscoding.Nanoseconds()),
		TranscodingU:       uint64(in.Transcoding.Nanoseconds()),
		TranscodedPreSendU: uint64(in.TranscodedPreSend.Nanoseconds()),
		SendingU:           uint64(in.Sending.Nanoseconds()),
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
		PreTranscoding:    nanosecondsToDuration(int64(in.PreTranscodingU)),
		Transcoding:       nanosecondsToDuration(int64(in.TranscodingU)),
		TranscodedPreSend: nanosecondsToDuration(int64(in.TranscodedPreSendU)),
		Sending:           nanosecondsToDuration(int64(in.SendingU)),
	}
}

func nanosecondsToDuration(ns int64) time.Duration {
	return time.Nanosecond * time.Duration(ns)
}
