package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/avpipeline/packet/condition/extra/quality"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
	avpipeline_proto "github.com/xaionaro-go/avpipeline/protobuf/avpipeline"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/goconv"
	"github.com/xaionaro-go/observability"
	"github.com/xaionaro-go/xgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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

func (c *Client) getGPRCDialParams() (target string, opts []grpc.DialOption) {
	target = c.Target
	opts = []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	parts := strings.SplitN(c.Target, ":", 2)
	if len(parts) < 2 {
		return
	}

	switch parts[0] {
	case "tcp+ssl":
		opts = []grpc.DialOption{
			grpc.WithTransportCredentials(
				credentials.NewTLS(&tls.Config{
					InsecureSkipVerify: true,
				}),
			),
		}
		target = parts[1]
	}
	return
}

func (c *Client) grpcClient() (ffstream_grpc.FFStreamClient, *grpc.ClientConn, error) {
	target, opts := c.getGPRCDialParams()
	conn, err := grpc.NewClient(target, opts...)
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

func (c *Client) GetAutoBitRateCalculator(
	ctx context.Context,
) (streammuxtypes.AutoBitRateCalculator, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := client.GetAutoBitRateCalculator(ctx, &ffstream_grpc.GetAutoBitRateCalculatorRequest{})
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}

	calc, err := goconv.AutoBitRateCalculatorFromGRPC(resp.GetCalculator())
	if err != nil {
		return nil, fmt.Errorf("unable to convert the auto bitrate calculator from gRPC: %v", err)
	}

	return calc, nil
}

func (c *Client) SetAutoBitRateCalculator(
	ctx context.Context,
	calculator streammuxtypes.AutoBitRateCalculator,
) (_err error) {
	logger.Debugf(ctx, "SetAutoBitRateCalculator(ctx, %#+v)", calculator)
	defer func() { logger.Debugf(ctx, "/SetAutoBitRateCalculator(ctx, %#+v): %v", calculator, _err) }()

	client, conn, err := c.grpcClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	calcGRPC, err := goconv.AutoBitRateCalculatorToGRPC(calculator)
	if err != nil {
		return fmt.Errorf("unable to convert the auto bitrate calculator to gRPC: %v", err)
	}

	logger.Tracef(ctx, "SetAutoBitRateCalculator: %s", try(json.Marshal(calcGRPC)))
	_, err = client.SetAutoBitRateCalculator(ctx, &ffstream_grpc.SetAutoBitRateCalculatorRequest{
		Calculator: calcGRPC,
	})
	if err != nil {
		return fmt.Errorf("query error: %w", err)
	}

	return nil
}

func (c *Client) GetFPSFraction(
	ctx context.Context,
) (uint32, uint32, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()

	resp, err := client.GetFPSFraction(ctx, &ffstream_grpc.GetFPSFractionRequest{})
	if err != nil {
		return 0, 0, fmt.Errorf("query error: %w", err)
	}

	return resp.GetNum(), resp.GetDen(), nil
}

func (c *Client) SetFPSFraction(
	ctx context.Context,
	num uint32,
	den uint32,
) error {
	client, conn, err := c.grpcClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = client.SetFPSFraction(ctx, &ffstream_grpc.SetFPSFractionRequest{
		Num: num,
		Den: den,
	})
	if err != nil {
		return fmt.Errorf("query error: %w", err)
	}

	return nil
}

func (c *Client) GetBitRates(
	ctx context.Context,
) (*streammuxtypes.BitRates, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := client.GetBitRates(ctx, &ffstream_grpc.GetBitRatesRequest{})
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}

	bitRates := goconv.BitRatesFromGRPC(resp.GetBitRates())
	return bitRates, nil
}

func (c *Client) GRPCClient(
	ctx context.Context,
) (ffstream_grpc.FFStreamClient, io.Closer, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return nil, nil, err
	}
	return client, conn, nil
}

func (c *Client) GetCallWrapper() xgrpc.CallWrapperFunc {
	return nil
}

func (c *Client) ProcessError(
	ctx context.Context,
	in error,
) error {
	return in
}

func (c *Client) Monitor(
	ctx context.Context,
	nodeID uint64,
	eventType avpipeline_proto.MonitorEventType,
	includePacketPayload bool,
	includeFramePayload bool,
	doDecode bool,
) (<-chan *avpipeline_proto.MonitorEvent, error) {
	return xgrpc.UnwrapChan(ctx,
		c,
		func(
			ctx context.Context,
			client ffstream_grpc.FFStreamClient,
		) (ffstream_grpc.FFStream_MonitorClient, error) {
			return client.Monitor(ctx, &avpipeline_proto.MonitorRequest{
				NodeId:               nodeID,
				EventType:            eventType,
				IncludePacketPayload: includePacketPayload,
				IncludeFramePayload:  includeFramePayload,
				DoDecode:             doDecode,
			})
		},
		func(
			ctx context.Context,
			ev *avpipeline_proto.MonitorEvent,
		) *avpipeline_proto.MonitorEvent {
			return ev
		},
	)
}

func (c *Client) GetLatencies(
	ctx context.Context,
) (*streammuxtypes.Latencies, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := client.GetLatencies(ctx, &ffstream_grpc.GetLatenciesRequest{})
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}

	latencies := goconv.LatenciesFromGRPC(resp.GetLatencies())
	return latencies, nil
}

func (c *Client) GetInputQuality(
	ctx context.Context,
) (*quality.QualityAggregated, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := client.GetInputQuality(ctx, &ffstream_grpc.GetInputQualityRequest{})
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}

	return &quality.QualityAggregated{
		Audio: goconv.StreamQualityFromGRPC(resp.GetAudio()),
		Video: goconv.StreamQualityFromGRPC(resp.GetVideo()),
	}, nil
}

func (c *Client) GetOutputQuality(
	ctx context.Context,
) (*quality.QualityAggregated, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := client.GetOutputQuality(ctx, &ffstream_grpc.GetOutputQualityRequest{})
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}

	return &quality.QualityAggregated{
		Audio: goconv.StreamQualityFromGRPC(resp.GetAudio()),
		Video: goconv.StreamQualityFromGRPC(resp.GetVideo()),
	}, nil
}
