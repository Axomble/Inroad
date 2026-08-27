package mail

import (
	"errors"
	"testing"
)

// TestParseEgressIP covers the source-bind parser's boundaries: empty is a
// no-op (OS default route), a valid IP round-trips, and garbage is rejected at
// wiring time rather than silently ignored.
func TestParseEgressIP(t *testing.T) {
	if addr, err := ParseEgressIP(""); err != nil || addr != nil {
		t.Fatalf("empty egress must be (nil,nil), got addr=%v err=%v", addr, err)
	}

	addr, err := ParseEgressIP("203.0.113.9")
	if err != nil || addr == nil {
		t.Fatalf("valid egress must parse, got addr=%v err=%v", addr, err)
	}
	if addr.IP.String() != "203.0.113.9" {
		t.Fatalf("egress ip = %q, want 203.0.113.9", addr.IP.String())
	}
	if addr.Port != 0 {
		t.Fatalf("egress port = %d, want 0 (ephemeral)", addr.Port)
	}

	if _, err := ParseEgressIP("not-an-ip"); err == nil {
		t.Fatal("garbage egress must be rejected, got nil error")
	}
}

// TestSendSourceBindDoesNotBypassSSRF is the security gate for spec §17.7: with
// a worker egress source bind set, a destination the SSRF guard blocks (loopback,
// the cloud metadata endpoint) STAYS blocked. The source bind is applied to the
// dialer only AFTER vetAddr vets the destination, so it can never relax the
// destination check. vetAddr runs first in Send, so a blocked host returns
// ErrHostNotPermitted before any dial is attempted.
func TestSendSourceBindDoesNotBypassSSRF(t *testing.T) {
	egress, err := ParseEgressIP("203.0.113.9") // documentation-range source
	if err != nil || egress == nil {
		t.Fatalf("egress parse: addr=%v err=%v", egress, err)
	}
	// allowPrivate=false = the strict multi-tenant Cloud posture.
	s := NewNetSender(false)
	s.LocalAddr = egress

	msg := Message{FromEmail: "from@acme.test", To: "to@acme.test", Subject: "x", BodyText: "y"}

	blocked := []struct {
		name string
		host string
	}{
		{"loopback", "127.0.0.1"},
		{"cloud metadata", "169.254.169.254"},
	}
	for _, tc := range blocked {
		if _, err := s.Send(SMTPConfig{Host: tc.host, Port: 587}, msg); !errors.Is(err, ErrHostNotPermitted) {
			t.Fatalf("%s destination must stay blocked with a source bind set, got err=%v", tc.name, err)
		}
	}
}

// TestInboxSourceBindDoesNotBypassSSRF is the §17.7 gate for the inbox dial path:
// the same source bind applied to IMAP polling never relaxes the destination vet.
func TestInboxSourceBindDoesNotBypassSSRF(t *testing.T) {
	egress, err := ParseEgressIP("203.0.113.9")
	if err != nil || egress == nil {
		t.Fatalf("egress parse: addr=%v err=%v", egress, err)
	}
	r := NewNetInboxReader(false)
	r.LocalAddr = egress

	if _, _, err := r.CurrentState(t.Context(), IMAPConfig{Host: "127.0.0.1", Port: 993}); !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("loopback IMAP destination must stay blocked with a source bind set, got err=%v", err)
	}
	if _, _, err := r.CurrentState(t.Context(), IMAPConfig{Host: "169.254.169.254", Port: 993}); !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("metadata IMAP destination must stay blocked with a source bind set, got err=%v", err)
	}
}
