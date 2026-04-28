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

// parseListenAddr splits a listen address of the form "<scheme>:<addr>"
// into its scheme and the remainder. When no scheme prefix is present
// the returned scheme is empty and the original input is returned as-is,
// which the caller treats as a unix-socket path.
func parseListenAddr(addr string) (scheme, rest string) {
	parts := strings.SplitN(addr, ":", 2)
	if len(parts) == 1 {
		return "", addr
	}
	return parts[0], parts[1]
}

func getListener(
	_ context.Context,
	addr string,
) (net.Listener, error) {
	scheme, rest := parseListenAddr(addr)

	if scheme == "" {
		return net.Listen("unix", rest)
	}

	switch scheme {
	case "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6", "unix", "unixpacket":
		return net.Listen(scheme, rest)
	case "tcp+ssl":
		// Strip the scheme so net.SplitHostPort accepts the remainder;
		// passing the full "tcp+ssl:host:port" to tls.Listen yields a
		// "too many colons in address" error.
	default:
		return nil, fmt.Errorf("unsupported listen scheme %q in %q", scheme, addr)
	}

	cert, err := cert.GenerateSelfSignedForServer()
	if err != nil {
		return nil, fmt.Errorf("failed to generate self-signed certificate: %w", err)
	}

	listener, err := tls.Listen("tcp", rest, &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create TLS listener at %s: %w", rest, err)
	}

	return listener, nil
}
