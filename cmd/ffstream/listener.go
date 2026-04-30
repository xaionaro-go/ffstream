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

// knownListenSchemes enumerates the prefixes that parseListenAddr
// recognizes as scheme tokens. An input whose colon-prefix is not in
// this set is treated as a bare addr (scheme returned empty) so the
// caller can apply its default interpretation — pure paths go to unix,
// host:port-style strings go to tcp. Keep in sync with the switch in
// getListener.
var knownListenSchemes = map[string]struct{}{
	"tcp":        {},
	"tcp4":       {},
	"tcp6":       {},
	"tcp+ssl":    {},
	"udp":        {},
	"udp4":       {},
	"udp6":       {},
	"unix":       {},
	"unixpacket": {},
}

// parseListenAddr splits a listen address of the form "<scheme>:<addr>"
// into its scheme and the remainder. The scheme is recognized only when
// the colon-prefix appears in knownListenSchemes; this lets a bare
// "host:port" (e.g. "127.0.0.1:3593") pass through with scheme="" and
// rest=addr so getListener can default it to tcp without colliding with
// the unix-socket interpretation reserved for pure paths (no colon).
//
// When no colon is present, the returned scheme is empty and the
// original input is returned as-is — getListener treats that case as a
// unix-socket path.
func parseListenAddr(addr string) (scheme, rest string) {
	parts := strings.SplitN(addr, ":", 2)
	if len(parts) == 1 {
		return "", addr
	}
	if _, ok := knownListenSchemes[parts[0]]; !ok {
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
		// No recognized scheme prefix. Disambiguate by content:
		// an addr containing ":" is a bare host:port (e.g.
		// "127.0.0.1:3593", "[::1]:3593") and defaults to tcp;
		// anything else is treated as a unix-socket path.
		if strings.Contains(rest, ":") {
			return net.Listen("tcp", rest)
		}
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
