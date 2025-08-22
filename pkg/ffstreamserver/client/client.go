package client

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/facebookincubator/go-belt/tool/logger"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/goconv"
	"github.com/xaionaro-go/observability"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type InputID uint64
type OutputID uint64

type Client struct {
	Target string
}

func New(target string) *Client {
	return &Client{Target: target}
}

func (c *Client) grpcClient() (ffstream_grpc.FFStreamClient, *grpc.ClientConn, error) {
	conn, err := grpc.NewClient(
		c.Target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to initialize a gRPC client: %w", err)
	}

	client := ffstream_grpc.NewFFStreamClient(conn)
	return client, conn, nil
}

func logLevelGo2Protobuf(logLevel logger.Level) ffstream_grpc.LoggingLevel {
	switch logLevel {
	case logger.LevelFatal:
		return ffstream_grpc.LoggingLevel_LoggingLevelFatal
	case logger.LevelPanic:
		return ffstream_grpc.LoggingLevel_LoggingLevelPanic
	case logger.LevelError:
		return ffstream_grpc.LoggingLevel_LoggingLevelError
	case logger.LevelWarning:
		return ffstream_grpc.LoggingLevel_LoggingLevelWarn
	case logger.LevelInfo:
		return ffstream_grpc.LoggingLevel_LoggingLevelInfo
	case logger.LevelDebug:
		return ffstream_grpc.LoggingLevel_LoggingLevelDebug
	case logger.LevelTrace:
		return ffstream_grpc.LoggingLevel_LoggingLevelTrace
	default:
		return ffstream_grpc.LoggingLevel_LoggingLevelWarn
	}
}

func (c *Client) SetLoggingLevel(
	ctx context.Context,
	logLevel logger.Level,
) error {
	client, conn, err := c.grpcClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = client.SetLoggingLevel(ctx, &ffstream_grpc.SetLoggingLevelRequest{
		Level: logLevelGo2Protobuf(logLevel),
	})
	if err != nil {
		return fmt.Errorf("query error: %w", err)
	}
	return nil
}

func (c *Client) RemoveOutput(
	ctx context.Context,
	outputID OutputID,
) error {
	client, conn, err := c.grpcClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = client.RemoveOutput(ctx, &ffstream_grpc.RemoveOutputRequest{
		Id: uint64(outputID),
	})
	if err != nil {
		return fmt.Errorf("query error: %w", err)
	}

	return nil
}

func (c *Client) GetRecoderConfig(
	ctx context.Context,
) (*streammuxtypes.RecoderConfig, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := client.GetRecoderConfig(ctx, &ffstream_grpc.GetRecoderConfigRequest{})
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}

	return ptr(goconv.RecoderConfigFromGRPC(resp.GetConfig())), nil
}

func (c *Client) SetRecoderConfig(
	ctx context.Context,
	cfg streammuxtypes.RecoderConfig,
) (_err error) {
	logger.Debugf(ctx, "SetRecoderConfig(ctx, %#+v)", cfg)
	defer func() { logger.Debugf(ctx, "/SetRecoderConfig(ctx, %#+v): %v", cfg, _err) }()
	client, conn, err := c.grpcClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = client.SetRecoderConfig(ctx, &ffstream_grpc.SetRecoderConfigRequest{
		Config: goconv.RecoderConfigToGRPC(cfg),
	})
	if err != nil {
		return fmt.Errorf("query error: %w", err)
	}

	return nil
}

func (c *Client) GetTolerableOutputQueueSizeBytes(
	ctx context.Context,
) (uint, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	resp, err := client.GetTolerableOutputQueueSizeBytes(
		ctx,
		&ffstream_grpc.GetTolerableOutputQueueSizeBytesRequest{},
	)
	if err != nil {
		return 0, fmt.Errorf("query error: %w", err)
	}

	return uint(resp.GetValue()), nil
}

func (c *Client) SetTolerableOutputQueueSizeBytes(
	ctx context.Context,
	sizeBytes uint,
) error {
	client, conn, err := c.grpcClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = client.SetTolerableOutputQueueSizeBytes(
		ctx,
		&ffstream_grpc.SetTolerableOutputQueueSizeBytesRequest{
			Value: uint64(sizeBytes),
		},
	)
	if err != nil {
		return fmt.Errorf("query error: %w", err)
	}

	return nil
}

func (c *Client) End(
	ctx context.Context,
) error {
	client, conn, err := c.grpcClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = client.End(ctx, &ffstream_grpc.EndRequest{})
	if err != nil {
		return fmt.Errorf("query error: %w", err)
	}

	return nil
}

func (c *Client) GetStats(
	ctx context.Context,
) (*ffstream_grpc.GetStatsReply, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := client.GetStats(ctx, &ffstream_grpc.GetStatsRequest{})
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}

	return resp, nil
}

func (c *Client) WaitChan(
	ctx context.Context,
) (<-chan struct{}, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return nil, err
	}

	waiter, err := client.WaitChan(ctx, &ffstream_grpc.WaitRequest{})
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}

	result := make(chan struct{})
	waiter.CloseSend()
	observability.Go(ctx, func(ctx context.Context) {
		defer conn.Close()
		defer func() {
			close(result)
		}()

		_, err := waiter.Recv()
		if err == io.EOF {
			logger.Debugf(ctx, "the receiver is closed: %v", err)
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Errorf(ctx, "unable to read data: %v", err)
			return
		}
	})

	return result, nil
}

func (c *Client) GetPipelines(
	ctx context.Context,
) (*ffstream_grpc.GetPipelinesResponse, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := client.GetPipelines(ctx, &ffstream_grpc.GetPipelinesRequest{})
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}

	return resp, nil
}
