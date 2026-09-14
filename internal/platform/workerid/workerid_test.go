package workerid

import (
	"net"
	"testing"
)

// TestFromIPMatchesAnIndependentUUIDv5Implementation pins FromIP against a
// value computed by a SEPARATE implementation of RFC 4122 UUIDv5, not just
// re-derived from this package's own code (which would only prove the code
// agrees with itself).
//
// This is the whole point of using a fixed namespace and the standard SHA-1
// UUIDv5 algorithm rather than a bespoke hash: an installer or provisioning
// script, written independently and possibly in a different language, must
// be able to compute the SAME worker id from the SAME IP with a one-line
// command — `uuidgen --sha1 --namespace 6ba7b810-9dad-11d1-80b4-00c04fd430c8
// --name 203.0.113.7` on a Linux host with util-linux's uuidgen (verified:
// macOS's BSD uuidgen has no --sha1/--namespace flags at all, so that exact
// command cannot be run on this development machine), or equivalently
// `python3 -c "import uuid; print(uuid.uuid5(uuid.UUID('6ba7b810-9dad-11d1-` +
// `80b4-00c04fd430c8')), '203.0.113.7'))"` anywhere Python 3 ships (which is
// what actually produced the expected value below — cross-checked against
// google/uuid's own NewSHA1 in a throwaway program; both agree).
//
// If this package and whatever provisions a host ever compute a DIFFERENT id
// for the same IP, every worker that comes up identifies itself under an id
// nobody assigned any mailbox to — and the failure is completely silent: the
// process starts, heartbeats, serves its queues, and simply never receives
// per-mailbox affinity work. Nothing errors, nothing logs a mismatch, because
// there is no third party to notice the two names disagree. Pinning the exact
// value here is what makes a future accidental change to the namespace,
// algorithm, or input encoding (e.g. swapping ip.String() for a raw byte
// slice) fail a test instead of silently reassigning the entire fleet's
// identities on the next deploy.
func TestFromIPMatchesAnIndependentUUIDv5Implementation(t *testing.T) {
	cases := []struct {
		name string
		ip   net.IP
		want string
	}{
		// python3 -c "import uuid; print(uuid.uuid5(uuid.UUID('6ba7b810-9dad-11d1-80b4-00c04fd430c8'), '203.0.113.7'))"
		{"ipv4", net.ParseIP("203.0.113.7"), "d7c28fd4-2ca6-5bc4-85e1-1a289f354d77"},
		// python3 -c "import uuid; print(uuid.uuid5(uuid.UUID('6ba7b810-9dad-11d1-80b4-00c04fd430c8'), '2001:db8::1'))"
		{"ipv6", net.ParseIP("2001:db8::1"), "826ff82d-1c4f-5cdd-a218-890654b75678"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FromIP(tc.ip)
			if got != tc.want {
				t.Fatalf("FromIP(%s) = %s, want %s (independently computed)", tc.ip, got, tc.want)
			}
		})
	}
}

// TestFromIPIsDeterministic proves the same address always yields the same
// id — the property that lets a reinstalled host keep its warmup affinity
// (spec F3): identity survives a process restart because it is RECOMPUTED
// from the same input, never stored and reused.
func TestFromIPIsDeterministic(t *testing.T) {
	ip := net.ParseIP("198.51.100.42")
	a := FromIP(ip)
	b := FromIP(ip)
	if a != b {
		t.Fatalf("FromIP(%s) not deterministic: %s != %s", ip, a, b)
	}
}

// TestFromIPDiffersByAddress proves two different IPs never collide onto the
// same id — the other half of the per-IP-reputation property: a host that
// genuinely got a new IP must be treated as a new sender, not silently
// inherit a stranger's identity.
func TestFromIPDiffersByAddress(t *testing.T) {
	a := FromIP(net.ParseIP("203.0.113.7"))
	b := FromIP(net.ParseIP("203.0.113.8"))
	if a == b {
		t.Fatalf("FromIP collided for two different addresses: both %s", a)
	}
}

func mustParseAddr(t *testing.T, s string) net.Addr {
	t.Helper()
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", s, err)
	}
	ipnet.IP = ip
	return ipnet
}

// TestPublicAddrPrefersIPv4OverIPv6 proves an interface list carrying both a
// public IPv4 and a public IPv6 address resolves to the IPv4 one — requirement
// 3 of the F3 spec ("derive from the public IPv4 when present, else the
// IPv6"), and order-independent: the IPv6 candidate is listed FIRST here so a
// naive "first match wins" implementation would get this wrong.
func TestPublicAddrPrefersIPv4OverIPv6(t *testing.T) {
	addrs := []net.Addr{
		mustParseAddr(t, "2001:db8::1/64"),
		mustParseAddr(t, "203.0.113.7/24"),
	}
	ip, family, ok := PublicAddr(addrs)
	if !ok {
		t.Fatal("PublicAddr found nothing, want the IPv4 candidate")
	}
	if family != FamilyIPv4 {
		t.Fatalf("family = %q, want %q", family, FamilyIPv4)
	}
	if ip.String() != "203.0.113.7" {
		t.Fatalf("ip = %s, want 203.0.113.7", ip)
	}
}

// TestPublicAddrFallsBackToIPv6WhenNoIPv4 covers requirement 3's other half:
// no public IPv4 at all, but a public IPv6 exists — used, and the family is
// recorded as ipv6 rather than silently reusing the ipv4 label.
func TestPublicAddrFallsBackToIPv6WhenNoIPv4(t *testing.T) {
	addrs := []net.Addr{
		mustParseAddr(t, "fe80::1/64"), // link-local, excluded
		mustParseAddr(t, "2001:db8::1/64"),
	}
	ip, family, ok := PublicAddr(addrs)
	if !ok {
		t.Fatal("PublicAddr found nothing, want the IPv6 candidate")
	}
	if family != FamilyIPv6 {
		t.Fatalf("family = %q, want %q", family, FamilyIPv6)
	}
	if ip.String() != "2001:db8::1" {
		t.Fatalf("ip = %s, want 2001:db8::1", ip)
	}
}

// TestPublicAddrExcludesEveryNonGlobalClass proves the NAT/no-egress case
// (requirement 4): loopback, link-local (including the cloud metadata
// address, which IS link-local), RFC1918 private, ULA, multicast and
// unspecified addresses are all rejected, so a self-hosted box behind a home
// router or a CI sandbox correctly finds nothing and falls back to the
// hostname rather than deriving an id from an address that identifies
// nothing about the box on the public internet.
func TestPublicAddrExcludesEveryNonGlobalClass(t *testing.T) {
	addrs := []net.Addr{
		mustParseAddr(t, "127.0.0.1/8"),
		mustParseAddr(t, "169.254.169.254/32"), // cloud metadata, link-local
		mustParseAddr(t, "10.0.0.5/8"),
		mustParseAddr(t, "172.16.0.5/12"),
		mustParseAddr(t, "192.168.1.5/24"),
		mustParseAddr(t, "224.0.0.1/4"),
		mustParseAddr(t, "0.0.0.0/32"),
		mustParseAddr(t, "::1/128"),
		mustParseAddr(t, "fe80::1/64"),
		mustParseAddr(t, "fc00::1/7"), // ULA
		mustParseAddr(t, "ff02::1/16"),
		mustParseAddr(t, "::/128"),
	}
	if ip, family, ok := PublicAddr(addrs); ok {
		t.Fatalf("PublicAddr found %s (%s) among an all-private/local address list, want none", ip, family)
	}
}

// TestPublicAddrIgnoresUnrecognisedAddrTypes proves an net.Addr implementation
// that is neither *net.IPNet nor *net.IPAddr (net.InterfaceAddrs only ever
// returns the former, but net.Addr is an interface any package could satisfy)
// is skipped rather than panicking a dereference.
type fakeAddr struct{ s string }

func (f fakeAddr) Network() string { return "fake" }
func (f fakeAddr) String() string  { return f.s }

func TestPublicAddrIgnoresUnrecognisedAddrTypes(t *testing.T) {
	addrs := []net.Addr{
		fakeAddr{"unrecognised"},
		mustParseAddr(t, "203.0.113.7/24"),
	}
	ip, _, ok := PublicAddr(addrs)
	if !ok || ip.String() != "203.0.113.7" {
		t.Fatalf("PublicAddr with an unrecognised addr type = (%v, %t), want (203.0.113.7, true)", ip, ok)
	}
}

// TestDefaultFallsBackToHostnameWithNoAddrs proves the NAT/no-egress/CI path:
// Default never errors and never blocks startup — it falls back to the
// hostname, exactly the pre-existing default before per-IP identity existed.
func TestDefaultFallsBackToHostnameWithNoAddrs(t *testing.T) {
	// Default calls net.InterfaceAddrs() itself; this test cannot control the
	// real host's interfaces, so it only asserts the documented contract that
	// is independent of the machine it runs on: SOME id and family come back,
	// and if the family is hostname the id equals the hostname exactly. The
	// interface-scanning logic itself is exercised address-list-first by
	// TestPublicAddr* above, which Default is a thin, untestable-by-value
	// wrapper around.
	id, family := Default("fallback-host")
	if id == "" {
		t.Fatal("Default returned an empty id")
	}
	if family == FamilyHostname && id != "fallback-host" {
		t.Fatalf("family=hostname but id = %q, want the hostname fallback-host", id)
	}
}
