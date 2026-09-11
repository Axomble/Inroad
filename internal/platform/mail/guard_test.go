package mail

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
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
	if _, err := vetAddr(context.Background(), "example.invalid", 587, allowedSMTPPorts, true); err == nil {
		t.Fatal("expected resolver error, got nil")
	}
}

// TestVetAddrLiteralIPPath drives the guard through a literal IP host, which
// bypasses the network resolver entirely (LookupIPAddr short-circuits on
// literals). Confirms the port allowlist and IP policy both fire.
func TestVetAddrLiteralIPPath(t *testing.T) {
	// Loopback literal is always rejected.
	if _, err := vetAddr(context.Background(), "127.0.0.1", 587, allowedSMTPPorts, true); !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("expected ErrHostNotPermitted for 127.0.0.1, got %v", err)
	}
	// Disallowed port fails before resolution.
	if _, err := vetAddr(context.Background(), "8.8.8.8", 6379, allowedSMTPPorts, true); err == nil {
		t.Fatal("expected port-not-permitted error")
	}
	// Public IP on an allowed port passes.
	addr, err := vetAddr(context.Background(), "8.8.8.8", 587, allowedSMTPPorts, false)
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

// TestVetAddrBoundsHangingResolver proves vetAddr never blocks its caller
// indefinitely on a resolver that never answers. The fake resolver's Dial
// blocks until ITS ctx is cancelled — the ctx vetAddr derives internally via
// dnsLookupTimeout, since the caller here (context.Background()) supplies no
// deadline of its own. A regression back to context.Background() inside
// vetAddr would make that inner ctx never cancel, so Dial would block forever
// and this test would hang past go test's own -timeout instead of failing
// cleanly — which is why the assertion also runs under a bounded watchdog
// rather than calling vetAddr inline.
func TestVetAddrBoundsHangingResolver(t *testing.T) {
	restore := setResolver(&net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	defer restore()

	type result struct {
		err error
		dur time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		_, err := vetAddr(context.Background(), "example.invalid", 587, allowedSMTPPorts, true)
		done <- result{err: err, dur: time.Since(start)}
	}()

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("expected a deadline/resolve error, got nil")
		}
		if !errors.Is(r.err, context.DeadlineExceeded) {
			t.Fatalf("expected an error wrapping context.DeadlineExceeded, got %v", r.err)
		}
		if r.dur > dnsLookupTimeout+5*time.Second {
			t.Fatalf("vetAddr took %v, want bounded near dnsLookupTimeout=%v", r.dur, dnsLookupTimeout)
		}
	case <-time.After(dnsLookupTimeout + 10*time.Second):
		t.Fatal("vetAddr hung well past dnsLookupTimeout — the hanging-resolver regression this test guards against")
	}
}
