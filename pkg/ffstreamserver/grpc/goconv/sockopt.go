//go:build with_libsrt
// +build with_libsrt

package goconv

import (
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/libsrt"
	"github.com/xaionaro-go/libsrt/sockopt"
)

func SRTSockoptIntFromGRPC(
	id ffstream_grpc.SRTFlagInt,
) (libsrt.Sockopt, bool) {
	switch id {
	case ffstream_grpc.SRTFlagInt_Latency:
		return sockopt.LATENCY, true
	}
	return sockopt.Sockopt(0), false
}

func SRTSockoptIntToGRPC(
	id libsrt.Sockopt,
) ffstream_grpc.SRTFlagInt {
	switch id {
	case sockopt.LATENCY:
		return ffstream_grpc.SRTFlagInt_Latency
	}
	return ffstream_grpc.SRTFlagInt_undefined
}
