// listener_test.go contains tests for the listen-address parser.
package main

import "testing"

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
