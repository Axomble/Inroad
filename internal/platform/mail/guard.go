package mail

import (
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

// vetAddr enforces the mail-port allowlist, resolves host, rejects
// dangerous/internal targets, and returns an ip:port string to dial. Dialing
// the resolved IP directly (callers keep the hostname as the TLS ServerName)
// closes the DNS-rebinding window between validation and connection.
func vetAddr(host string, port int, allowedPorts map[int]bool, allowPrivate bool) (string, error) {
	if !allowedPorts[port] {
		return "", fmt.Errorf("port %d not permitted for this protocol", port)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("resolve host: %w", err)
	}
	if len(ips) == 0 {
		return "", ErrHostNotPermitted
	}
	ip := ips[0]
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return "", ErrHostNotPermitted
	}
	if !allowPrivate && ip.IsPrivate() {
		return "", ErrHostNotPermitted
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}
