package mail

import (
	"testing"
	"time"
)

func TestSendRejectsLoopbackHost(t *testing.T) {
	s := &NetSender{Timeout: time.Second}
	_, err := s.Send(SMTPConfig{Host: "127.0.0.1", Port: 587, UseTLS: true, Username: "u", Password: "p"},
		Message{FromEmail: "a@x.com", To: "b@y.com", Subject: "hi", BodyText: "hello"})
	if err != ErrHostNotPermitted {
		t.Fatalf("expected ErrHostNotPermitted, got %v", err)
	}
}
