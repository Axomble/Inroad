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

// The IMAP counterpart. Both ports are exercised because 143 dials plain and
// upgrades via STARTTLS while 993 dials through tls.Dialer — two different dial
// calls in dialIMAP, either of which could regress alone.
//
// go-imap's client.DialWithDialer / DialWithDialerTLS take no context at all, so
// asserting context.Canceled is what distinguishes the hand-rolled dial from the
// library call it replaced.
func TestIMAPAbortsTheDialWhenTheCallerGoesAway(t *testing.T) {
	for _, port := range []int{143, 993} {
		t.Run(fmt.Sprintf("port %d", port), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			tester := &NetTester{Timeout: 30 * time.Second}
			start := time.Now()
			err := tester.TestIMAP(ctx, IMAPConfig{Host: "203.0.113.10", Port: port})

			if !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v, want a context.Canceled dial error", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("took %v to notice a cancelled context against a 30s timeout", elapsed)
			}
		})
	}
}

// The worker's path, and a different claim from the test above: that the ctx the
// poll handler holds actually REACHES the dial, rather than being accepted by the
// signature and dropped. MarkRead and Rescue took a `_ context.Context` and
// discarded it for exactly that reason, so the wiring is the part worth asserting.
func TestInboxReaderDialHonoursTheCallersCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	reader := &NetInboxReader{Timeout: 30 * time.Second}
	start := time.Now()
	_, _, err := reader.CurrentState(ctx, IMAPConfig{Host: "203.0.113.10", Port: 993})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want a context.Canceled dial error", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v; the reader is not passing its ctx to the dial", elapsed)
	}
}

// MarkRead and Rescue already accepted a context and named it `_`, so the compiler
// was satisfied and a cancelled worker still waited out the full dial. These assert
// the parameter is now load-bearing. Rescue needs a non-INBOX SourceFolder or it
// returns early without dialing at all.
func TestEngagerDialHonoursTheCallersCancellation(t *testing.T) {
	target := EngageTarget{
		Provider: "smtp", IMAPHost: "203.0.113.10", IMAPPort: 993,
		SourceFolder: "Junk", MessageID: "<probe@example.com>",
	}
	engage := map[string]func(context.Context, EngageTarget) error{}
	e := &NetEngager{Timeout: 30 * time.Second}
	engage["MarkRead"], engage["Rescue"] = e.MarkRead, e.Rescue

	for name, fn := range engage {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			start := time.Now()
			err := fn(ctx, target)

			if !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v, want a context.Canceled dial error", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("took %v; the ctx is being accepted and discarded", elapsed)
			}
		})
	}
}

func TestIMAPRejectsPrivateWhenDisallowed(t *testing.T) {
	tester := &NetTester{Timeout: time.Second} // AllowPrivate false
	err := tester.TestIMAP(t.Context(), IMAPConfig{Host: "10.0.0.5", Port: 993})
	if !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("expected ErrHostNotPermitted for private IP, got %v", err)
	}
}
