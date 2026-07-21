package mail

import (
	"testing"
	"time"

	gomail "github.com/wneessen/go-mail"
)

func TestSendRejectsLoopbackHost(t *testing.T) {
	s := &NetSender{Timeout: time.Second}
	_, err := s.Send(SMTPConfig{Host: "127.0.0.1", Port: 587, UseTLS: true, Username: "u", Password: "p"},
		Message{FromEmail: "a@x.com", To: "b@y.com", Subject: "hi", BodyText: "hello"})
	if err != ErrHostNotPermitted {
		t.Fatalf("expected ErrHostNotPermitted, got %v", err)
	}
}

func TestBuildMessageSetsThreadingHeaders(t *testing.T) {
	m, err := buildMessage(Message{
		FromEmail: "a@x.com", To: "b@y.com", Subject: "Re: Hi", BodyText: "yo",
		InReplyTo: "<root@x>", References: "<root@x> <p2@x>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.GetGenHeader(gomail.HeaderInReplyTo); len(got) != 1 || got[0] != "<root@x>" {
		t.Fatalf("In-Reply-To = %v", got)
	}
	if got := m.GetGenHeader(gomail.HeaderReferences); len(got) != 1 || got[0] != "<root@x> <p2@x>" {
		t.Fatalf("References = %v", got)
	}
}

func TestBuildMessageOmitsThreadingWhenEmpty(t *testing.T) {
	m, err := buildMessage(Message{FromEmail: "a@x.com", To: "b@y.com", Subject: "Hi", BodyText: "yo"})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.GetGenHeader(gomail.HeaderInReplyTo); len(got) != 0 {
		t.Fatalf("expected no In-Reply-To on a root message, got %v", got)
	}
	if got := m.GetGenHeader(gomail.HeaderReferences); len(got) != 0 {
		t.Fatalf("expected no References on a root message, got %v", got)
	}
}
