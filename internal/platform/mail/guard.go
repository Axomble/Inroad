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
