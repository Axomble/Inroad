package mail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
)

// ErrHostNotPermitted is returned when a mailbox host resolves to an address
// blocked by SSRF protection: loopback, link-local (incl. the cloud metadata
// endpoint 169.254.169.254), unspecified, multicast, and — unless private
// hosts are allowed — RFC1918 / ULA ranges.
var ErrHostNotPermitted = errors.New("mail host not permitted")

var allowedSMTPPorts = map[int]bool{25: true, 465: true, 587: true, 2525: true}
var allowedIMAPPorts = map[int]bool{143: true, 993: true}

// resolver is the DNS resolver used by vetAddr. Overridable in tests via
// setResolver to inject a fake that simulates DNS rebinding.
var resolver = net.DefaultResolver

// setResolver swaps the package-level resolver for the duration of a test.
// It returns a restore function the caller should defer.
func setResolver(r *net.Resolver) func() {
	prev := resolver
	resolver = r
	return func() { resolver = prev }
}

// vetAddr enforces the mail-port allowlist, resolves host, rejects
// dangerous/internal targets, and returns an ip:port string to dial. Dialing
// the resolved IP directly (callers keep the hostname as the TLS ServerName)
// closes the DNS-rebinding window between validation and connection.
//
// Every resolved IP is checked: a single disallowed record in the answer set
// fails the whole vet. The returned ip:port is the first *allowed* IP; the
// caller should dial exactly that address (never re-resolve the hostname).
func vetAddr(host string, port int, allowedPorts map[int]bool, allowPrivate bool) (string, error) {
	if !allowedPorts[port] {
		return "", fmt.Errorf("port %d not permitted for this protocol", port)
	}
	ips, err := resolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return "", fmt.Errorf("resolve host: %w", err)
	}
	if len(ips) == 0 {
		return "", ErrHostNotPermitted
	}
	for _, ipAddr := range ips {
		if !ipAllowed(ipAddr.IP, allowPrivate) {
			return "", ErrHostNotPermitted
		}
	}
	return net.JoinHostPort(ips[0].IP.String(), strconv.Itoa(port)), nil
}

// ClassifyHost resolves a user-supplied HTTP(S) host and classifies it for
// SSRF policy, sharing this package's resolver and address taxonomy with the
// mail dials. It is the vetting seam behind AI-provider base URLs
// (agent-platform spec §3), where the policy differs from mail in one way:
// loopback is not always-blocked but treated as PRIVATE, because the entire
// point of the operator opt-in (AI_ALLOW_PRIVATE_BASE_URL) is a localhost
// Ollama/vLLM. Link-local (incl. the cloud metadata endpoint), unspecified,
// and multicast remain unconditionally hostile and return an error.
//
// The caller decides what "private" means for its context (reject unless the
// operator flag is set). Like vetAddr, every resolved IP is checked and a
// single hostile record fails the whole classification.
func ClassifyHost(ctx context.Context, host string) (private bool, err error) {
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return false, fmt.Errorf("resolve host: %w", err)
	}
	private, err = classifyIPs(ips)
	return private, err
}

// classifyIPs applies the HTTP-host taxonomy to a resolved answer set: any
// unconditionally hostile record (link-local incl. cloud metadata, multicast,
// unspecified, or an empty answer) fails the whole set; loopback/private
// records mark the host private.
func classifyIPs(ips []net.IPAddr) (private bool, err error) {
	if len(ips) == 0 {
		return false, ErrHostNotPermitted
	}
	for _, ipAddr := range ips {
		ip := ipAddr.IP
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			return false, ErrHostNotPermitted
		}
		if ip.IsLoopback() || ip.IsPrivate() {
			private = true
		}
	}
	return private, nil
}

// GuardedDialContext returns a net DialContext that enforces the ClassifyHost
// policy at DIAL time and connects to the vetted IP directly (closing the
// DNS-rebinding window between validation and connection, like vetAddr): the
// destination host is resolved, every record is checked — link-local (incl.
// cloud metadata), multicast, and unspecified are always rejected; loopback
// and private ranges are rejected unless allowPrivate — and the first allowed
// IP is dialed. TLS verification still uses the request's original hostname
// (http.Transport keeps it as the ServerName). This is the transport seam
// behind AI-provider discovery/calls to user-supplied endpoints.
func GuardedDialContext(allowPrivate bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("split host/port: %w", err)
		}
		// ONE resolution feeds both the policy check and the dial target, so a
		// rebinding DNS server cannot answer differently between them.
		ips, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve host: %w", err)
		}
		private, err := classifyIPs(ips)
		if err != nil {
			return nil, err
		}
		if private && !allowPrivate {
			return nil, fmt.Errorf("%w: private host %q", ErrHostNotPermitted, host)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
}

// ipAllowed reports whether ip is permitted by the SSRF policy: loopback,
// link-local (incl. cloud metadata), unspecified, multicast are always
// blocked; RFC1918/ULA is blocked unless allowPrivate is set.
func ipAllowed(ip net.IP, allowPrivate bool) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if !allowPrivate && ip.IsPrivate() {
		return false
	}
	return true
}
