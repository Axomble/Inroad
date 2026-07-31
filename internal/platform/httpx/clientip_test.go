package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClientIP is the anti-spoof matrix for the X-Forwarded-For resolver. The
// security-critical rows are the untrusted-peer case (XFF must be ignored) and
// the injected-leftmost-spoof case (the RIGHTMOST non-trusted hop wins, never the
// attacker-controlled leftmost value).
func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		trusted    []string
		remoteAddr string
		xff        string // "" = header absent
		want       string
	}{
		{
			// (a) Untrusted direct peer: XFF is forgeable, so it is IGNORED and the
			// peer address is used. This is the core anti-spoof guarantee.
			name:       "untrusted peer ignores XFF",
			trusted:    nil,
			remoteAddr: "198.51.100.10:5555",
			xff:        "203.0.113.9",
			want:       "198.51.100.10",
		},
		{
			// Even WITH a trusted set, a peer outside it is untrusted → XFF ignored.
			name:       "peer outside trusted set ignores XFF",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "198.51.100.10:5555",
			xff:        "203.0.113.9",
			want:       "198.51.100.10",
		},
		{
			// (b) Trusted direct peer, single client hop → that client.
			name:       "trusted peer single hop",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.0.0.1:5555",
			xff:        "203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			// (c) Multi-hop client,proxyA,proxyB with both proxies trusted →
			// rightmost-non-trusted = client.
			name:       "multi hop peels trusted proxies",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.0.0.1:5555",
			xff:        "203.0.113.9, 10.9.9.9, 10.0.0.2",
			want:       "203.0.113.9",
		},
		{
			// (d) Attacker injects a spoofed leftmost value; only the edge proxy is
			// trusted → the RIGHTMOST (real) client is returned, NOT the spoof.
			name:       "leftmost spoof rejected",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.0.0.1:5555",
			xff:        "1.2.3.4, 203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			// Whole chain trusted → fall back to the direct peer.
			name:       "all hops trusted falls back to peer",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.0.0.1:5555",
			xff:        "10.9.9.9, 10.0.0.2",
			want:       "10.0.0.1",
		},
		{
			// (e) Malformed entries are skipped without panic; the valid rightmost
			// non-trusted hop still wins.
			name:       "malformed hop skipped",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.0.0.1:5555",
			xff:        "203.0.113.9, not-an-ip",
			want:       "203.0.113.9",
		},
		{
			name:       "empty XFF trusted peer uses peer",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.0.0.1:5555",
			xff:        "",
			want:       "10.0.0.1",
		},
		{
			name:       "entirely malformed XFF falls back to peer",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.0.0.1:5555",
			xff:        "garbage, , also-bad",
			want:       "10.0.0.1",
		},
		{
			// (f) IPv6: bracketed host:port is stripped and the address canonicalized.
			name:       "ipv6 peer port stripped",
			trusted:    nil,
			remoteAddr: "[2001:db8::1]:5555",
			xff:        "203.0.113.9",
			want:       "2001:db8::1",
		},
		{
			// (f) IPv6 trusted proxy resolves an IPv6 client OUTSIDE its /32.
			name:       "ipv6 trusted proxy resolves client",
			trusted:    []string{"2001:db8::/32"},
			remoteAddr: "[2001:db8::2]:5555",
			xff:        "2606:4700::1111",
			want:       "2606:4700::1111",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			r.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			got := NewClientIPResolver(tc.trusted).ClientIP(r)
			if got.String() != tc.want {
				t.Fatalf("ClientIP = %q, want %q", got.String(), tc.want)
			}
		})
	}
}

// TestClientIPEmptyRemoteAddr proves a request with an unparseable RemoteAddr and
// no usable header yields the zero Addr (so an allowlist caller fails closed)
// rather than panicking.
func TestClientIPEmptyRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r.RemoteAddr = ""
	if got := NewClientIPResolver(nil).ClientIP(r); got.IsValid() {
		t.Fatalf("ClientIP = %q, want zero Addr", got.String())
	}
}
