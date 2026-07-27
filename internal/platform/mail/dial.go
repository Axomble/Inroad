package mail

import (
	"fmt"
	"net"
)

// ParseEgressIP converts an optional worker egress IP into a *net.TCPAddr for
// net.Dialer.LocalAddr, binding only the SOURCE address of outbound SMTP/IMAP
// dials (spec §15). An empty ip returns (nil, nil): the dialer then uses the OS
// default route (single-node dev). A malformed ip is rejected at wiring time.
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
