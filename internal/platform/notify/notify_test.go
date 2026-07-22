package notify

import (
	"context"
	"strings"
	"testing"
)

func TestConsoleSenderCaptures(t *testing.T) {
	var got Message
	s := &consoleSender{sink: func(m Message) { got = m }}
	m := Message{To: "a@b.io", Subject: "Hi", TextBody: "body", HTMLBody: "<p>body</p>"}
	if err := s.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if got.To != "a@b.io" || got.Subject != "Hi" {
		t.Fatalf("not captured: %+v", got)
	}
}

func TestVerifyEmailRendersLink(t *testing.T) {
	m := VerifyEmail("https://app.test/verify-email?token=abc")
	if !strings.Contains(m.TextBody, "https://app.test/verify-email?token=abc") ||
		!strings.Contains(m.HTMLBody, "abc") || m.Subject == "" {
		t.Fatalf("verify template missing link/subject: %+v", m)
	}
}

func TestResetEmailRendersLink(t *testing.T) {
	m := ResetEmail("https://app.test/reset-password?token=xyz")
	if !strings.Contains(m.TextBody, "https://app.test/reset-password?token=xyz") ||
		!strings.Contains(m.HTMLBody, "xyz") || m.Subject == "" {
		t.Fatalf("reset template missing link/subject: %+v", m)
	}
}

func TestInviteEmailRendersLink(t *testing.T) {
	m := InviteEmail("Acme Co", "https://app.test/accept-invite?token=inv")
	if !strings.Contains(m.TextBody, "https://app.test/accept-invite?token=inv") ||
		!strings.Contains(m.HTMLBody, "inv") || m.Subject == "" {
		t.Fatalf("invite template missing link/subject: %+v", m)
	}
}

func TestInviteEmailEscapesHTML(t *testing.T) {
	m := InviteEmail("<script>x</script>", "https://app.test/accept-invite?token=inv")
	if strings.Contains(m.HTMLBody, "<script>x</script>") {
		t.Fatalf("invite HTML body contains unescaped workspace name: %+v", m)
	}
	if !strings.Contains(m.HTMLBody, "&lt;script&gt;") {
		t.Fatalf("invite HTML body missing escaped workspace name: %+v", m)
	}
	if !strings.Contains(m.TextBody, "<script>x</script>") {
		t.Fatalf("invite text body should keep workspace name literal: %+v", m)
	}
}

func TestNewSMTPRequiresHostAndFrom(t *testing.T) {
	if _, err := New(Config{Driver: "smtp"}); err == nil {
		t.Fatal("expected error for smtp driver with empty host/from, got nil")
	}
}

func TestNewUnknownDriverErrors(t *testing.T) {
	if _, err := New(Config{Driver: "carrier-pigeon"}); err == nil {
		t.Fatal("expected error for unknown driver, got nil")
	}
}
