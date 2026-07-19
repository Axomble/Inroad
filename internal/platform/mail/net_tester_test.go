package mail

import (
	"testing"
	"time"
)

// SSRF guard tests — these need no network: literal IPs resolve without DNS and
// the guard rejects them before any dial.

func TestSMTPRejectsLoopback(t *testing.T) {
	tester := &NetTester{Timeout: time.Second} // AllowPrivate false
	err := tester.TestSMTP(SMTPConfig{Host: "127.0.0.1", Port: 587, UseTLS: true})
	if err != ErrHostNotPermitted {
		t.Fatalf("expected ErrHostNotPermitted for loopback, got %v", err)
	}
}

func TestSMTPRejectsCloudMetadataLinkLocal(t *testing.T) {
	tester := &NetTester{Timeout: time.Second, AllowPrivate: true} // even with private allowed
	err := tester.TestSMTP(SMTPConfig{Host: "169.254.169.254", Port: 587, UseTLS: true})
	if err != ErrHostNotPermitted {
		t.Fatalf("expected ErrHostNotPermitted for link-local metadata IP, got %v", err)
	}
}

func TestSMTPRejectsDisallowedPort(t *testing.T) {
	tester := &NetTester{Timeout: time.Second, AllowPrivate: true}
	err := tester.TestSMTP(SMTPConfig{Host: "203.0.113.10", Port: 6379, UseTLS: true})
	if err == nil {
		t.Fatal("expected error for non-mail port, got nil")
	}
}

func TestIMAPRejectsPrivateWhenDisallowed(t *testing.T) {
	tester := &NetTester{Timeout: time.Second} // AllowPrivate false
	err := tester.TestIMAP(IMAPConfig{Host: "10.0.0.5", Port: 993})
	if err != ErrHostNotPermitted {
		t.Fatalf("expected ErrHostNotPermitted for private IP, got %v", err)
	}
}
