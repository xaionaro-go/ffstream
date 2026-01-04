//go:build with_libsrt
// +build with_libsrt

// grpc_server_libsrt.go implements SRT-specific gRPC methods for the FFStream server.

package ffstreamserver

import (
	"context"

	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/goconv"
	"github.com/xaionaro-go/libsrt"
	"github.com/xaionaro-go/libsrt/threadsafe"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (srv *GRPCServer) GetOutputSRTStats(
	ctx context.Context,
	req *ffstream_grpc.GetOutputSRTStatsRequest,
) (*ffstream_grpc.GetOutputSRTStatsReply, error) {
	var stats *libsrt.Tracebstats
	err := srv.FFStream.WithSRTOutput(ctx, int(req.GetOutputId()), func(sock *threadsafe.Socket) error {
		result, err := sock.Bistats(false, true)
		if err == nil {
			stats = ptr(result.Convert())
		}
		return err
	})
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to get the output SRT statistics: %v", err)
	}

	return goconv.OutputSRTStatsToGRPC(stats), nil
}

func (srv *GRPCServer) SetFlagInt(
	ctx context.Context,
	req *ffstream_grpc.SetSRTFlagIntRequest,
) (*ffstream_grpc.SetSRTFlagIntReply, error) {
	sockOpt, ok := goconv.SRTSockoptIntFromGRPC(req.GetFlag())
	if !ok {
		return nil, status.Errorf(codes.Unknown, "unknown SRT socket option: %d", req.GetFlag())
	}

	err := srv.FFStream.WithSRTOutput(ctx, int(req.GetOutputId()), func(sock *threadsafe.Socket) error {
		v := libsrt.BlobInt(req.GetValue())
		return sock.Setsockflag(sockOpt, &v)
	})
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to set the SRT socket option: %v", err)
	}

	return &ffstream_grpc.SetSRTFlagIntReply{}, nil
}

func (srv *GRPCServer) GetFlagInt(
	ctx context.Context,
	req *ffstream_grpc.GetSRTFlagIntRequest,
) (*ffstream_grpc.GetSRTFlagIntReply, error) {
	sockOpt, ok := goconv.SRTSockoptIntFromGRPC(req.GetFlag())
	if !ok {
		return nil, status.Errorf(codes.Unknown, "unknown SRT socket option: %d", req.GetFlag())
	}

	var v libsrt.BlobInt
	err := srv.FFStream.WithSRTOutput(ctx, int(req.GetOutputId()), func(sock *threadsafe.Socket) error {
		return sock.Getsockflag(sockOpt, &v)
	})
	if err != nil {
		return nil, status.Errorf(codes.Unknown, "unable to set the SRT socket option: %v", err)
	}

	return &ffstream_grpc.GetSRTFlagIntReply{
		Value: int64(v),
	}, nil
}
