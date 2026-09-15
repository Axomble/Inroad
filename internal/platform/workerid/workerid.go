// Package workerid derives a fleet worker's stable identity from its host's
// public IP address rather than its hostname. Reputation in cold email is
// per-IP: a host that is reinstalled keeps its IP and should keep its
// identity — and its warmup/send affinity along with it; a host that gets a
// new IP is genuinely a new sender and must not inherit the old one's
// assignments. A container hostname, by contrast, changes on
// `--force-recreate`, silently stranding the per-worker affinity queue that
// used to be keyed on it — a failure this project has already hit.
//
// Every derived id is a UUIDv5 (RFC 4122, SHA-1) in the DNS namespace
// (6ba7b810-9dad-11d1-80b4-00c04fd430c8). RFC 4122 defines only four fixed
// namespaces (DNS/URL/OID/X.500); DNS is simply the conventional choice for
// "an opaque short string that is neither a URL, an OID, nor an X.500 DN",
// which an IP address literal is. Using a FIXED namespace and the STANDARD
// algorithm — not a bespoke hash — is the entire point: an installer or
// provisioning script, on a different host and possibly in a different
// language, can compute the exact same id from the same IP independently.
// See FromIP's doc for how to verify that by hand, and its test for why a
// silent mismatch here is dangerous specifically because it produces no
// error anywhere.
package workerid

import (
	"net"

	"github.com/google/uuid"
)

// Namespace is the fixed DNS namespace UUID (RFC 4122 Appendix C) every
// derived worker id is minted under.
var Namespace = uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")

// Family records which identity source produced a worker's id. Persisted
// alongside worker_id on the `workers` heartbeat row so an operator can tell
// a NAT'd or hostname-derived worker from an IP-derived one without
// cross-referencing logs — see the F3 spec's observability requirement.
type Family string

const (
	// FamilyIPv4 / FamilyIPv6: derived from a globally-routable address found
	// on a local interface. IPv4 is preferred when both are present (see
	// PublicAddr).
	FamilyIPv4 Family = "ipv4"
	FamilyIPv6 Family = "ipv6"
	// FamilyHostname: no public address was found on any local interface
	// (NAT, no egress interface, CI, a container without host networking) —
	// falls back to the OS hostname, exactly this project's behaviour before
	// per-IP identity existed. Startup must never fail over this: a
	// self-hoster behind NAT still has to be able to run Inroad.
	FamilyHostname Family = "hostname"
	// FamilyOverride: an operator pinned INROAD_WORKER_ID explicitly.
	// Recorded so the `workers` row shows a human decided the id, not a
	// heuristic — and so a test fixture's fixed id is distinguishable from a
	// real derivation on the same row.
	FamilyOverride Family = "override"
)

// FromIP computes the deterministic UUIDv5 for an IP address's string form
// (net.IP.String(): dotted-quad for IPv4, RFC 5952 shorthand for IPv6). The
// SAME address always yields the SAME id — the property this whole package
// exists for.
//
// To compute this independently of the binary (e.g. to verify a provisioning
// script agrees, per the package doc): on a Linux host with util-linux's
// uuidgen, `uuidgen --sha1 --namespace 6ba7b810-9dad-11d1-80b4-00c04fd430c8
// --name <ip>`; anywhere Python 3 ships, `python3 -c "import uuid;
// print(uuid.uuid5(uuid.UUID('6ba7b810-9dad-11d1-80b4-00c04fd430c8'),
// '<ip>'))"`. macOS's BSD uuidgen supports neither flag, which is why the
// package test pins against the Python form instead — see
// TestFromIPMatchesAnIndependentUUIDv5Implementation for the exact values and
// why they matter.
func FromIP(ip net.IP) string {
	return uuid.NewSHA1(Namespace, []byte(ip.String())).String()
}

// PublicAddr picks the address a derived identity is built from: the first
// globally-routable IPv4 address in addrs, or — only when no IPv4 candidate
// exists — the first globally-routable IPv6 address. It is a pure function of
// addrs (rather than calling net.InterfaceAddrs() itself) so it is unit
// testable against a fabricated interface list without touching the host's
// real network stack; Default is the thin I/O wrapper around it.
//
// "Globally routable" excludes loopback, link-local (which includes the cloud
// metadata address 169.254.169.254), private RFC1918/ULA, multicast, and
// unspecified. This package does not reuse mail.vetAddr for that
// classification: vetAddr answers "is it safe to DIAL this remote address",
// a question about a host we are about to connect to; this answers "is this
// address of MINE public", a question about a local interface — same
// underlying net.IP predicates, different question, and pulling in an SSRF
// guard for a check that never dials anything would be the wrong dependency
// for the wrong reason.
func PublicAddr(addrs []net.Addr) (ip net.IP, family Family, ok bool) {
	var v6 net.IP
	for _, a := range addrs {
		candidate := ipFromAddr(a)
		if candidate == nil || !isGloballyRoutable(candidate) {
			continue
		}
		if v4 := candidate.To4(); v4 != nil {
			return v4, FamilyIPv4, true // IPv4 wins immediately — see the doc above.
		}
		if v6 == nil {
			v6 = candidate // remember the first IPv6 candidate; keep scanning for IPv4.
		}
	}
	if v6 != nil {
		return v6, FamilyIPv6, true
	}
	return nil, "", false
}

// ipFromAddr extracts the net.IP from the two concrete types
// net.InterfaceAddrs() actually returns (*net.IPNet for most interfaces,
// *net.IPAddr for some point-to-point ones). Any other net.Addr
// implementation is skipped rather than dereferenced — net.Addr is an
// interface, and nothing here should assume only the standard library ever
// satisfies it.
func ipFromAddr(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	default:
		return nil
	}
}

// isGloballyRoutable reports whether ip is usable as a public identity: not
// loopback, not link-local (unicast or multicast, which covers the cloud
// metadata address), not private (RFC1918 IPv4 / ULA IPv6, via net.IP's own
// IsPrivate), not multicast, not unspecified.
func isGloballyRoutable(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsInterfaceLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

// Default resolves a worker's default identity when no explicit
// INROAD_WORKER_ID override is configured: the id derived from the host's
// public IP if PublicAddr finds one among the local interfaces, else the
// hostname. Falling back rather than failing is deliberate (F3 requirement
// 4): a self-hoster behind NAT, with no egress-facing interface address at
// all, must still be able to start.
//
// net.InterfaceAddrs failing is treated identically to finding no public
// address — neither is worth failing startup over, and the caller
// (config.Load) has no logger constructed yet to report a harder failure
// with anyway; cmd/worker logs the resolved family at INFO once its logger
// exists.
func Default(hostname string) (id string, family Family) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return hostname, FamilyHostname
	}
	ip, fam, ok := PublicAddr(addrs)
	if !ok {
		return hostname, FamilyHostname
	}
	return FromIP(ip), fam
}
