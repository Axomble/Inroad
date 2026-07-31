package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIPResolver derives the real client IP from an incoming request, correctly
// walking the X-Forwarded-For chain from the RIGHT past trusted-proxy hops.
//
// Trust is opt-in. With NO trusted proxies configured, X-Forwarded-For is ignored
// entirely and RemoteAddr is used — an untrusted direct peer can forge any header,
// so trusting XFF from one would let a caller spoof its source IP (and defeat an
// api-key IP allowlist). A conforming reverse proxy (nginx
// `$proxy_add_x_forwarded_for`, ALB, Traefik) APPENDS the peer it received from to
// the RIGHT of the chain, so the real client is the RIGHTMOST entry that is not
// itself a trusted proxy; the leftmost value is attacker-controlled and must never
// be taken.
type ClientIPResolver struct {
	trustedProxies []netip.Prefix
}

// NewClientIPResolver parses the trusted-proxy CIDRs, silently dropping
// unparseable entries (loudness belongs at startup config, not per-request). An
// empty list yields a resolver that always returns RemoteAddr — the safe default.
func NewClientIPResolver(trustedProxies []string) ClientIPResolver {
	nets := make([]netip.Prefix, 0, len(trustedProxies))
	for _, c := range trustedProxies {
		if p, err := netip.ParsePrefix(c); err == nil {
			nets = append(nets, p.Masked())
		}
	}
	return ClientIPResolver{trustedProxies: nets}
}

// ClientIP returns the resolved client IP, or the zero Addr (IsValid() == false)
// if it cannot be determined. Callers that treat the result as an access-control
// boundary must fail closed on an invalid Addr.
func (c ClientIPResolver) ClientIP(r *http.Request) netip.Addr {
	direct := parseAddr(hostOnly(r.RemoteAddr))
	// No trusted proxies, or the direct peer is not itself a trusted proxy: the
	// peer IS the client and XFF (which it could forge) is ignored.
	if len(c.trustedProxies) == 0 || !c.isTrusted(direct) {
		return direct
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return direct
	}
	// The direct peer is a trusted proxy. Peel entries off the RIGHT while they are
	// themselves trusted proxies; the first non-trusted entry is the real client.
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a := parseAddr(strings.TrimSpace(parts[i]))
		if !a.IsValid() {
			continue // malformed hop: skip and keep walking left
		}
		if c.isTrusted(a) {
			continue // another known proxy hop: keep peeling
		}
		return a // rightmost non-trusted entry = the origin client
	}
	// Every hop was a trusted proxy (or malformed): fall back to the direct peer,
	// itself trusted — the best identity available.
	return direct
}

func (c ClientIPResolver) isTrusted(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	for _, p := range c.trustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// hostOnly strips the port from a RemoteAddr ("host:port" or "[ipv6]:port"),
// returning it unchanged when it has no port. net.SplitHostPort correctly unwraps
// bracketed IPv6, unlike a naive split on ":".
func hostOnly(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// parseAddr parses s into a netip.Addr, unmapping any IPv4-in-IPv6 form so a
// prefix comparison happens in the address's native family. An invalid input
// yields the zero Addr (IsValid() == false).
func parseAddr(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}
