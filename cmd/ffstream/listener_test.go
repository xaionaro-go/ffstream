// listener_test.go contains tests for the listen-address parser.
package main

import (
	"context"
	"net"
	"testing"
)

func TestParseListenAddr(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantScheme string
		wantRest   string
	}{
		{"tcp", "tcp:127.0.0.1:3593", "tcp", "127.0.0.1:3593"},
		{"tcpSSL", "tcp+ssl:127.0.0.1:3593", "tcp+ssl", "127.0.0.1:3593"},
		{"unixPath", "/var/run/ffstream.sock", "", "/var/run/ffstream.sock"},
		{"udp6", "udp6:[::1]:3593", "udp6", "[::1]:3593"},
		// Bare host:port — no recognized scheme prefix. parseListenAddr
		// must return scheme="" so getListener can default it to tcp;
		// pre-fix, this returned scheme="127.0.0.1" (bogus) and the
		// switch fell to the "unsupported listen scheme" error in
		// task #158.
		{"bareHostPort_v4", "127.0.0.1:3593", "", "127.0.0.1:3593"},
		{"bareHostPort_zeroAddr", "0.0.0.0:8080", "", "0.0.0.0:8080"},
		{"bareHostPort_v6", "[::1]:3593", "", "[::1]:3593"},
		{"bareHostPort_emptyHost", ":3593", "", ":3593"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			scheme, rest := parseListenAddr(tc.input)
			if scheme != tc.wantScheme || rest != tc.wantRest {
				t.Fatalf("parseListenAddr(%q) = (%q, %q); want (%q, %q)",
					tc.input, scheme, rest, tc.wantScheme, tc.wantRest)
			}
		})
	}
}

func TestListener_TCPSSLStripsPrefix(t *testing.T) {
	const input = "tcp+ssl:127.0.0.1:3593"
	const wantRest = "127.0.0.1:3593"

	scheme, rest := parseListenAddr(input)
	if scheme != "tcp+ssl" {
		t.Fatalf("scheme = %q; want %q", scheme, "tcp+ssl")
	}
	if rest != wantRest {
		t.Fatalf("rest = %q; want %q (so tls.Listen does not see %q and reject it as 'too many colons')",
			rest, wantRest, input)
	}
}

// TestGetListener_BareHostPort regresses task #158: a listen address
// without a recognized scheme prefix (e.g. "127.0.0.1:0") must be
// accepted as a bare host:port and bound on tcp. Pre-fix, the code
// erred with `unsupported listen scheme "127.0.0.1"`.
func TestGetListener_BareHostPort(t *testing.T) {
	cases := []string{
		"127.0.0.1:0",
		"[::1]:0",
		":0",
	}
	for _, addr := range cases {
		addr := addr
		t.Run(addr, func(t *testing.T) {
			ln, err := getListener(context.Background(), addr)
			if err != nil {
				t.Fatalf("getListener(%q) failed: %v "+
					"(must accept bare host:port as tcp; #158)", addr, err)
			}
			defer ln.Close()
			if _, ok := ln.(*net.TCPListener); !ok {
				t.Fatalf("getListener(%q) returned %T; want *net.TCPListener", addr, ln)
			}
		})
	}
}

// TestGetListener_TCPPrefixStillWorks pins the existing tcp:host:port
// shape: explicit schemes must not regress.
func TestGetListener_TCPPrefixStillWorks(t *testing.T) {
	ln, err := getListener(context.Background(), "tcp:127.0.0.1:0")
	if err != nil {
		t.Fatalf("getListener(\"tcp:127.0.0.1:0\") failed: %v", err)
	}
	defer ln.Close()
	if _, ok := ln.(*net.TCPListener); !ok {
		t.Fatalf("getListener returned %T; want *net.TCPListener", ln)
	}
}

// TestGetListener_UnsupportedSchemeRejected pins the rejection path:
// a colon-prefixed addr whose prefix is neither a known scheme nor
// usable as a bare host:port part must still error. We test by using
// a scheme-looking token followed by something that net.Listen("tcp",
// ...) would accept (so the bare-host:port fallback would silently
// succeed if we regressed) — and assert that getListener's known-scheme
// gate refuses to misinterpret it.
//
// Note: under the new rule, "anything:port" with port-looking suffix
// becomes a bare addr (and tcp.Listen succeeds when port is numeric).
// This test therefore only covers the case of a scheme that *does*
// look like a scheme. We use a known-bad scheme that gets through
// parseListenAddr only if parseListenAddr regresses to returning
// scheme="garbage".
func TestGetListener_UnsupportedSchemeRejected(t *testing.T) {
	// "garbage:127.0.0.1:0": parseListenAddr returns scheme="" rest=full;
	// getListener sees ":" in rest → tcp.Listen("garbage:127.0.0.1:0")
	// errs because "garbage" isn't a numeric port. We assert that an
	// error is surfaced (rather than silently succeeding); the message
	// itself is net.Listen's, not ours.
	_, err := getListener(context.Background(), "garbage:127.0.0.1:0")
	if err == nil {
		t.Fatal("expected error for malformed bare addr; got nil")
	}
}
