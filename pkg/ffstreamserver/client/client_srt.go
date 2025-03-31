//go:build with_libsrt
// +build with_libsrt

package client

import (
	"context"
	"fmt"

	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/go/ffstream_grpc"
	"github.com/xaionaro-go/ffstream/pkg/ffstreamserver/grpc/goconv"
	"github.com/xaionaro-go/libsrt"
)

func (c *Client) GetOutputSRTStats(
	ctx context.Context,
) (*libsrt.Tracebstats, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	resp, err := client.GetOutputSRTStats(ctx, &ffstream_grpc.GetOutputSRTStatsRequest{})
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}

	return goconv.OutputSRTStatsFromGRPC(resp), nil
}

func (c *Client) GetFlagInt(
	ctx context.Context,
	flag libsrt.Sockopt,
) (int64, error) {
	client, conn, err := c.grpcClient()
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	flagID := goconv.SockoptIntToGRPC(flag)
	if flagID == ffstream_grpc.FlagInt_undefined {
		return 0, fmt.Errorf("unknown flag: %v", flag)
	}

	resp, err := client.GetFlagInt(ctx, &ffstream_grpc.GetFlagIntRequest{
		Flag: flagID,
	})
	if err != nil {
		return 0, fmt.Errorf("query error: %w", err)
	}

	return resp.GetValue(), nil
}

func (c *Client) SetFlagInt(
	ctx context.Context,
	flag libsrt.Sockopt,
	value int64,
) error {
	client, conn, err := c.grpcClient()
	if err != nil {
		return err
	}
	defer conn.Close()

	flagID := goconv.SockoptIntToGRPC(flag)
	if flagID == ffstream_grpc.FlagInt_undefined {
		return fmt.Errorf("unknown flag: %v", flag)
	}

	_, err = client.SetFlagInt(ctx, &ffstream_grpc.SetFlagIntRequest{
		Flag:  flagID,
		Value: value,
	})
	if err != nil {
		return fmt.Errorf("query error: %w", err)
	}

	return nil
}
