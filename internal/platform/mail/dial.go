package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
)

// ParseEgressIP converts an optional worker egress IP into a *net.TCPAddr for
// net.Dialer.LocalAddr, binding only the SOURCE address of this worker's
// outbound provider dials (spec §15) — SMTP and IMAP directly, Gmail and
// Microsoft Graph through the dialer inside newAPIHTTPClient. An empty ip
// returns (nil, nil): the dialer then uses the OS default route (single-node
// dev). A malformed ip is rejected at wiring time.
//
// SECURITY (spec §17.7): the result sets the SOURCE address ONLY. It never
// influences destination selection — every dial still resolves and vetAddr-vets
// the destination host and connects to that vetted IP. Binding a source can only
// pick which local interface egresses; it can never reach a destination the SSRF
// guard blocks. Port is left 0 so the OS assigns an ephemeral source port.
func ParseEgressIP(ip string) (*net.TCPAddr, error) {
	if ip == "" {
		return nil, nil
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return nil, fmt.Errorf("mail: invalid worker egress ip %q", ip)
	}
	return &net.TCPAddr{IP: parsed}, nil
}

// dialIMAPTransport opens the transport an IMAP session runs over — plain TCP on
// port 143 (dialIMAP upgrades it via STARTTLS afterwards) and implicit TLS on
// every other allowed port — and is the ONE hook the mail tests replace to reach
// an in-process IMAP server.
//
// SECURITY: this is a socket-level seam, not a policy seam. vetAddr has already
// run, already applied the port allowlist, already resolved the host and already
// rejected (or approved) every returned IP by the time this is called; addr is
// its verdict. Swapping the hook therefore changes WHERE THE SOCKET GOES in a
// test process, and cannot widen what a mailbox host is allowed to be — a
// loopback or link-local host still fails the vet before this runs, which
// TestTransportSeamDoesNotBypassTheSSRFGuard asserts.
//
// The alternative — no seam — is why this package had no fake IMAP server at
// all: every compatibility bug here is a "works against my provider, fails
// against yours" bug that cannot be demonstrated against a real provider, and
// the guard correctly refuses 127.0.0.1.
var dialIMAPTransport = netDialIMAPTransport

// netDialIMAPTransport is the production dialIMAPTransport: the dial dialIMAP
// performed inline before the seam existed. cfg.Host is kept as the TLS
// ServerName even though addr is the resolved IP, so certificate validation
// still checks the hostname the caller asked for.
func netDialIMAPTransport(ctx context.Context, addr string, cfg IMAPConfig, dialer *net.Dialer) (net.Conn, error) {
	if cfg.Port == 143 {
		return dialer.DialContext(ctx, "tcp", addr)
	}
	tlsDialer := &tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: cfg.Host}}
	return tlsDialer.DialContext(ctx, "tcp", addr)
}

// dialSMTPTransport is the SMTP counterpart of dialIMAPTransport, shared by the
// connection tester and the sender, and replaced by the same tests for the same
// reason. Everything dialIMAPTransport's SECURITY note says applies verbatim:
// addr is vetAddr's verdict, reached only after the vet passed.
//
// Unlike the IMAP hook this is PLAIN TCP only, because its single caller —
// newSMTPClient's dial func — hands the conn to gomail, which applies implicit
// TLS on 465 and STARTTLS elsewhere from its own policy and would otherwise
// double-wrap it.
var dialSMTPTransport = netDialSMTPTransport

// netDialSMTPTransport is the production dialSMTPTransport.
func netDialSMTPTransport(ctx context.Context, addr string, dialer *net.Dialer) (net.Conn, error) {
	return dialer.DialContext(ctx, "tcp", addr)
}
