// Package ffstreamserver provides a gRPC server for controlling and monitoring FFStream.
//
// ffstream_server.go defines the main FFStreamServer struct and its serving logic.
package ffstreamserver

import (
	"context"
	"net"
	"runtime/debug"
	"time"

	"github.com/facebookincubator/go-belt"
	"github.com/facebookincubator/go-belt/tool/experimental/errmon"
	grpc_recovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/observability"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const gracefulStopTimeout = 5 * time.Second

type FFStreamServer struct {
	ffStream            *ffstream.FFStream
	stopTranscodingFunc context.CancelFunc
}

func New(ffStream *ffstream.FFStream) *FFStreamServer {
	return &FFStreamServer{
		ffStream: ffStream,
	}
}

func NewWithStop(
	ffStream *ffstream.FFStream,
	stopTranscodingFunc context.CancelFunc,
) *FFStreamServer {
	return &FFStreamServer{
		ffStream:            ffStream,
		stopTranscodingFunc: stopTranscodingFunc,
	}
}

func (s *FFStreamServer) methodNeedsRuntime(method string) bool {
	switch method {
	case ffstream_grpc.FFStream_End_FullMethodName:
		return false
	default:
		return true
	}
}

func (s *FFStreamServer) requireRuntimeReady(method string) error {
	if !s.methodNeedsRuntime(method) {
		return nil
	}
	if s.ffStream != nil && s.ffStream.IsRuntimeReady() {
		return nil
	}
	return status.Error(codes.Unavailable, "ffstream runtime is not ready")
}

func (s *FFStreamServer) runtimeReadinessUnaryInterceptor(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	if err := s.requireRuntimeReady(info.FullMethod); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (s *FFStreamServer) runtimeReadinessStreamInterceptor(
	srv any,
	stream grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	if err := s.requireRuntimeReady(info.FullMethod); err != nil {
		return err
	}
	return handler(srv, stream)
}

func (s *FFStreamServer) ServeContext(
	ctx context.Context,
	listener net.Listener,
) error {
	opts := []grpc_recovery.Option{
		grpc_recovery.WithRecoveryHandler(func(p any) (err error) {
			ctx = belt.WithField(ctx, "stack_trace", string(debug.Stack()))
			errmon.ObserveRecoverCtx(ctx, p)
			return nil
		}),
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			s.runtimeReadinessUnaryInterceptor,
			grpc_recovery.UnaryServerInterceptor(opts...),
		),
		grpc.ChainStreamInterceptor(
			s.runtimeReadinessStreamInterceptor,
			grpc_recovery.StreamServerInterceptor(opts...),
		),
	)
	ffstreamGRPC := NewGRPCServer(ctx, s.ffStream)
	ffstreamGRPC.stopTranscodingFunc = s.stopTranscodingFunc
	ffstream_grpc.RegisterFFStreamServer(grpcServer, ffstreamGRPC)

	ctx, cancelFn := context.WithCancel(ctx)
	defer cancelFn()
	observability.Go(ctx, func(ctx context.Context) {
		<-ctx.Done()
		stopped := make(chan struct{})
		observability.Go(context.WithoutCancel(ctx), func(ctx context.Context) {
			grpcServer.GracefulStop()
			close(stopped)
		})

		timer := time.NewTimer(gracefulStopTimeout)
		defer timer.Stop()
		select {
		case <-stopped:
		case <-timer.C:
			grpcServer.Stop()
		}
	})
	return grpcServer.Serve(listener)
}
