//go:build with_libsrt
// +build with_libsrt

package goconv

import (
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/libsrt"
	"github.com/xaionaro-go/libsrt/sockopt"
)

func SockoptIntFromGRPC(
	id ffstream_grpc.FlagInt,
) (libsrt.Sockopt, bool) {
	switch id {
	case ffstream_grpc.FlagInt_Latency:
		return sockopt.LATENCY, true
	}
	return sockopt.Sockopt(0), false
}

func SockoptIntToGRPC(
	id libsrt.Sockopt,
) ffstream_grpc.FlagInt {
	switch id {
	case sockopt.LATENCY:
		return ffstream_grpc.FlagInt_Latency
	}
	return ffstream_grpc.FlagInt_undefined
}
