// listener.go provides functions to create network listeners.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"

	"github.com/xaionaro-go/ffstream/pkg/cert"
)

func getListener(
	_ context.Context,
	addr string,
) (net.Listener, error) {
	parts := strings.SplitN(addr, ":", 2)

	if len(parts) == 1 {
		return net.Listen("unix", addr)
	}

	switch parts[0] {
	case "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6", "unix", "unixpacket":
		return net.Listen(parts[0], parts[1])
	case "tcp+ssl":
	}

	cert, err := cert.GenerateSelfSignedForServer()
	if err != nil {
		return nil, fmt.Errorf("failed to generate self-signed certificate: %w", err)
	}

	listener, err := tls.Listen("tcp", addr, &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create TLS listener at %s: %w", addr, err)
	}

	return listener, nil
}
