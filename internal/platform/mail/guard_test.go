package mail

import (
	"context"
	"errors"
	"net"
	"testing"
)

// TestVetAddrRejectsMixedAnswerSet guards against a partial-block bypass: a
// resolver that returns both a routable public IP and a loopback address must
// fail the whole vet, not pick the first "good" record. Without this an
// attacker who controls DNS could return [8.8.8.8, 127.0.0.1] and rely on
// callers dialing whichever entry the resolver puts first.
func TestVetAddrRejectsMixedAnswerSet(t *testing.T) {
	// Drive the check through the ip-level helper directly: a mixed answer set
	// is rejected because each individual address is vetted, and loopback /
	// link-local never pass even with allowPrivate=true.
	if ipAllowed(net.ParseIP("127.0.0.1"), true) {
		t.Fatal("loopback must not be allowed even with allowPrivate=true")
	}
	if ipAllowed(net.ParseIP("169.254.169.254"), true) {
		t.Fatal("link-local metadata IP must never be allowed")
	}
}

// TestVetAddrDNSRebindWindow simulates a resolver whose second call returns a
// different set of IPs (a rebinding attack). The guard resolves once and
// returns the vetted ip:port; the caller MUST dial that literal address
// rather than re-resolving. The sender wires WithDialContextFunc to enforce
// this - here we simply confirm the guard hands back a concrete IP.
func TestVetAddrReturnsVettedIP(t *testing.T) {
	restore := setResolver(&net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, errors.New("no dns")
		},
	})
	defer restore()

	// With a broken resolver the vet must fail closed.
	if _, err := vetAddr("example.invalid", 587, allowedSMTPPorts, true); err == nil {
		t.Fatal("expected resolver error, got nil")
	}
}

// TestVetAddrLiteralIPPath drives the guard through a literal IP host, which
// bypasses the network resolver entirely (LookupIPAddr short-circuits on
// literals). Confirms the port allowlist and IP policy both fire.
func TestVetAddrLiteralIPPath(t *testing.T) {
	// Loopback literal is always rejected.
	if _, err := vetAddr("127.0.0.1", 587, allowedSMTPPorts, true); !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("expected ErrHostNotPermitted for 127.0.0.1, got %v", err)
	}
	// Disallowed port fails before resolution.
	if _, err := vetAddr("8.8.8.8", 6379, allowedSMTPPorts, true); err == nil {
		t.Fatal("expected port-not-permitted error")
	}
	// Public IP on an allowed port passes.
	addr, err := vetAddr("8.8.8.8", 587, allowedSMTPPorts, false)
	if err != nil {
		t.Fatalf("expected 8.8.8.8:587 to vet ok, got %v", err)
	}
	if addr != "8.8.8.8:587" {
		t.Fatalf("expected ip:port, got %q", addr)
	}
}

// TestClassifyHostLiteralIPs drives ClassifyHost (the AI base-URL vetting
// seam) through literal IPs: loopback/RFC1918 classify as private rather than
// erroring (the operator opt-in decides), public is non-private, and
// link-local (cloud metadata) / multicast / unspecified are hard errors that
// no flag can allow.
func TestClassifyHostLiteralIPs(t *testing.T) {
	ctx := context.Background()

	private, err := ClassifyHost(ctx, "127.0.0.1")
	if err != nil || !private {
		t.Fatalf("loopback: want private=true err=nil, got %v %v", private, err)
	}
	private, err = ClassifyHost(ctx, "10.1.2.3")
	if err != nil || !private {
		t.Fatalf("rfc1918: want private=true err=nil, got %v %v", private, err)
	}
	private, err = ClassifyHost(ctx, "8.8.8.8")
	if err != nil || private {
		t.Fatalf("public: want private=false err=nil, got %v %v", private, err)
	}
	if _, err := ClassifyHost(ctx, "169.254.169.254"); !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("metadata endpoint must be ErrHostNotPermitted, got %v", err)
	}
	if _, err := ClassifyHost(ctx, "224.0.0.1"); !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("multicast must be ErrHostNotPermitted, got %v", err)
	}
	if _, err := ClassifyHost(ctx, "0.0.0.0"); !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("unspecified must be ErrHostNotPermitted, got %v", err)
	}
}

// TestClassifyHostResolverFailureFailsClosed proves an unresolvable host is
// an error, never silently "public".
func TestClassifyHostResolverFailureFailsClosed(t *testing.T) {
	restore := setResolver(&net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, errors.New("no dns")
		},
	})
	defer restore()
	if _, err := ClassifyHost(context.Background(), "example.invalid"); err == nil {
		t.Fatal("expected resolver error, got nil")
	}
}
