package mail

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	gomail "github.com/wneessen/go-mail"
)

func TestSendRejectsLoopbackHost(t *testing.T) {
	s := &NetSender{Timeout: time.Second}
	_, err := s.Send(SMTPConfig{Host: "127.0.0.1", Port: 587, Username: "u", Password: "p"},
		Message{FromEmail: "a@x.com", To: "b@y.com", Subject: "hi", BodyText: "hello"})
	if !errors.Is(err, ErrHostNotPermitted) {
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

// serialize renders m to its RFC822 wire form the way an SMTP transport would,
// so header assertions verify what actually lands on the wire (not just the
// in-memory gomail state) — §7 warmup receipt detection (C5) reads the
// X-Inroad-Warmup header off the delivered message.
func serialize(t *testing.T, m *gomail.Msg) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	return buf.String()
}

func TestBuildMessageSerializesExtraHeaderToWire(t *testing.T) {
	const token = "wtok-abc123"
	m, err := buildMessage(Message{
		FromEmail: "a@x.com", To: "b@y.com", Subject: "Hi", BodyText: "yo",
		ExtraHeaders: map[string]string{"X-Inroad-Warmup": token},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire := serialize(t, m); !strings.Contains(wire, "X-Inroad-Warmup: "+token) {
		t.Fatalf("expected X-Inroad-Warmup header with token %q on the wire, got:\n%s", token, wire)
	}
}

func TestBuildMessageOmitsExtraHeaderOnWireWhenNil(t *testing.T) {
	m, err := buildMessage(Message{FromEmail: "a@x.com", To: "b@y.com", Subject: "Hi", BodyText: "yo"})
	if err != nil {
		t.Fatal(err)
	}
	if wire := serialize(t, m); strings.Contains(wire, "X-Inroad-Warmup") {
		t.Fatalf("nil ExtraHeaders must not emit any X-Inroad-Warmup header, got:\n%s", wire)
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
