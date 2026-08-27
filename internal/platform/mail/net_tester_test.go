package mail

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// SSRF guard tests — these need no network: literal IPs resolve without DNS and
// the guard rejects them before any dial.

func TestSMTPRejectsLoopback(t *testing.T) {
	tester := &NetTester{Timeout: time.Second} // AllowPrivate false
	err := tester.TestSMTP(t.Context(), SMTPConfig{Host: "127.0.0.1", Port: 587})
	if !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("expected ErrHostNotPermitted for loopback, got %v", err)
	}
}

func TestSMTPRejectsCloudMetadataLinkLocal(t *testing.T) {
	tester := &NetTester{Timeout: time.Second, AllowPrivate: true} // even with private allowed
	err := tester.TestSMTP(t.Context(), SMTPConfig{Host: "169.254.169.254", Port: 587})
	if !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("expected ErrHostNotPermitted for link-local metadata IP, got %v", err)
	}
}

func TestSMTPRejectsDisallowedPort(t *testing.T) {
	tester := &NetTester{Timeout: time.Second, AllowPrivate: true}
	err := tester.TestSMTP(t.Context(), SMTPConfig{Host: "203.0.113.10", Port: 6379})
	if err == nil {
		t.Fatal("expected error for non-mail port, got nil")
	}
}

// A cancelled caller must abort the dial, not leave us holding a socket to a
// stranger's server for the full timeout.
//
// The assertion is on context.Canceled specifically, not on elapsed time: a dial
// bounded only by a timeout (net.DialTimeout, tls.DialWithDialer) cannot produce
// that error under any network conditions, so this fails if the DialContext calls
// are reverted — whether the reverted dial then hangs or fails fast. Both ports are
// exercised because implicit TLS and STARTTLS go through different dialers.
func TestSMTPAbortsTheDialWhenTheCallerGoesAway(t *testing.T) {
	for _, port := range []int{587, 465} {
		t.Run(fmt.Sprintf("port %d", port), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel() // the caller has already disconnected

			// TEST-NET-3: routable as far as the SSRF guard is concerned, so the dial is
			// genuinely attempted, and black-holed in practice so only cancellation ends it.
			tester := &NetTester{Timeout: 30 * time.Second}
			start := time.Now()
			err := tester.TestSMTP(ctx, SMTPConfig{Host: "203.0.113.10", Port: port})

			if !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v, want a context.Canceled dial error", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("took %v to notice a cancelled context against a 30s timeout", elapsed)
			}
		})
	}
}

func TestIMAPRejectsPrivateWhenDisallowed(t *testing.T) {
	tester := &NetTester{Timeout: time.Second} // AllowPrivate false
	err := tester.TestIMAP(IMAPConfig{Host: "10.0.0.5", Port: 993})
	if !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("expected ErrHostNotPermitted for private IP, got %v", err)
	}
}
