package mail

import (
	"testing"
	"time"
)

// These tests need no server: dialing a closed port must surface an error
// rather than panic or silently pass.

func TestSMTPDialFailureIsReported(t *testing.T) {
	tester := &NetTester{Timeout: time.Second}
	if err := tester.TestSMTP(SMTPConfig{Host: "127.0.0.1", Port: 1, UseTLS: true}); err == nil {
		t.Fatal("expected SMTP dial error for closed port, got nil")
	}
}

func TestIMAPDialFailureIsReported(t *testing.T) {
	tester := &NetTester{Timeout: time.Second}
	if err := tester.TestIMAP(IMAPConfig{Host: "127.0.0.1", Port: 1}); err == nil {
		t.Fatal("expected IMAP dial error for closed port, got nil")
	}
}
