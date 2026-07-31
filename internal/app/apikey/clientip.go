package apikey

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// clientIPResolver derives the caller's IP from a request, honoring
// X-Forwarded-For / X-Real-IP ONLY when the direct peer is a configured trusted
// proxy. Trusting those headers unconditionally would let any caller spoof their
// source IP and defeat a key's allowlist, so trust is opt-in via the same
// INROAD_TRUSTED_PROXIES list the identity handler uses.
//
// This mirrors identity's per-request logic rather than importing it: the
// identity version is bound to its Handler and unexported, and the IP-allowlist
// check is a security boundary that is clearer owning its own small, tested
// resolver than reaching across a domain seam.
type clientIPResolver struct {
	trustedProxies []netip.Prefix
}

// newClientIPResolver parses the trusted-proxy CIDRs, silently dropping
// unparseable entries (loudness belongs at startup config, not per-request).
func newClientIPResolver(trustedProxies []string) clientIPResolver {
	nets := make([]netip.Prefix, 0, len(trustedProxies))
	for _, c := range trustedProxies {
		if p, err := netip.ParsePrefix(c); err == nil {
			nets = append(nets, p.Masked())
		}
	}
	return clientIPResolver{trustedProxies: nets}
}

// resolve returns the client IP, or the zero Addr if it cannot be determined.
func (c clientIPResolver) resolve(r *http.Request) netip.Addr {
	direct := parseAddr(remoteHost(r.RemoteAddr))
	if c.isTrusted(direct) {
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			// Leftmost = original client; anything to the right is a proxy hop.
			first := v
			if i := strings.IndexByte(v, ','); i >= 0 {
				first = v[:i]
			}
			if a := parseAddr(strings.TrimSpace(first)); a.IsValid() {
				return a
			}
		}
		if v := r.Header.Get("X-Real-IP"); v != "" {
			if a := parseAddr(strings.TrimSpace(v)); a.IsValid() {
				return a
			}
		}
	}
	return direct
}

func (c clientIPResolver) isTrusted(addr netip.Addr) bool {
	if !addr.IsValid() || len(c.trustedProxies) == 0 {
		return false
	}
	for _, p := range c.trustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// remoteHost strips the port from a RemoteAddr ("host:port" or "[ipv6]:port"),
// returning it unchanged when it has no port.
func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// parseAddr parses s into a netip.Addr, unmapping any IPv4-in-IPv6 form so a
// prefix comparison is done in the address's native family. An invalid input
// yields the zero Addr (IsValid() == false).
func parseAddr(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}
