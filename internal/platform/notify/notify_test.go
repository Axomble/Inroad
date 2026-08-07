package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	gomail "github.com/wneessen/go-mail"
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
	m := VerifyEmail("user@acme.test", "https://app.test/verify-email?token=abc")
	if !strings.Contains(m.TextBody, "https://app.test/verify-email?token=abc") ||
		!strings.Contains(m.HTMLBody, "abc") || m.Subject == "" {
		t.Fatalf("verify template missing link/subject: %+v", m)
	}
}

func TestResetEmailRendersLink(t *testing.T) {
	m := ResetEmail("user@acme.test", "https://app.test/reset-password?token=xyz")
	if !strings.Contains(m.TextBody, "https://app.test/reset-password?token=xyz") ||
		!strings.Contains(m.HTMLBody, "xyz") || m.Subject == "" {
		t.Fatalf("reset template missing link/subject: %+v", m)
	}
}

func TestInviteEmailRendersLink(t *testing.T) {
	m := InviteEmail("invitee@other.test", "Acme Co", "https://app.test/accept-invite?token=inv")
	if !strings.Contains(m.TextBody, "https://app.test/accept-invite?token=inv") ||
		!strings.Contains(m.HTMLBody, "inv") || m.Subject == "" {
		t.Fatalf("invite template missing link/subject: %+v", m)
	}
}

// TestTemplatesAddressTheRecipient pins the invariant whose absence shipped
// every transactional email with an empty envelope recipient: each template
// constructor takes the recipient and puts it on the Message.
func TestTemplatesAddressTheRecipient(t *testing.T) {
	const to = "recipient@acme.test"
	for name, m := range map[string]Message{
		"VerifyEmail":    VerifyEmail(to, "https://app.test/verify-email?token=abc"),
		"ResetEmail":     ResetEmail(to, "https://app.test/reset-password?token=xyz"),
		"LoginCodeEmail": LoginCodeEmail(to, "123456"),
		"InviteEmail":    InviteEmail(to, "Acme Co", "https://app.test/accept-invite?token=inv"),
	} {
		if m.To != to {
			t.Errorf("%s: To = %q, want %q", name, m.To, to)
		}
	}
}

func TestInviteEmailEscapesHTML(t *testing.T) {
	m := InviteEmail("invitee@other.test", "<script>x</script>", "https://app.test/accept-invite?token=inv")
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

// TestSendRejectsEmptyRecipient covers the belt-and-braces half of the fix:
// the template constructors make an unaddressed Message impossible to build,
// and any Message literal assembled by hand still fails loud at the seam
// rather than being delivered nowhere.
func TestSendRejectsEmptyRecipient(t *testing.T) {
	var delivered int
	s := requireRecipient{next: &consoleSender{sink: func(Message) { delivered++ }}}

	err := s.Send(context.Background(), Message{Subject: "Verify your email", TextBody: "body"})
	if !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("Send with empty To: got %v, want ErrNoRecipient", err)
	}
	if delivered != 0 {
		t.Fatalf("unaddressed message was handed to the driver %d time(s)", delivered)
	}

	if err := s.Send(context.Background(), Message{To: "a@b.io", Subject: "Hi", TextBody: "body"}); err != nil {
		t.Fatalf("Send with a recipient: %v", err)
	}
	if delivered != 1 {
		t.Fatalf("addressed message delivered %d time(s), want 1", delivered)
	}
}

// TestNewGuardsEveryDriver confirms the guard is applied by New, so no driver
// it hands out can deliver an unaddressed message — the console driver would
// otherwise log an empty recipient and return nil, which is exactly how the
// original bug stayed invisible in dev.
func TestNewGuardsEveryDriver(t *testing.T) {
	for _, cfg := range []Config{
		{Driver: "console", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		{Driver: "smtp", SMTPHost: "smtp.test", From: "sys@acme.test"},
	} {
		s, err := New(cfg)
		if err != nil {
			t.Fatalf("New(%q): %v", cfg.Driver, err)
		}
		if err := s.Send(context.Background(), Message{Subject: "x", TextBody: "y"}); !errors.Is(err, ErrNoRecipient) {
			t.Errorf("driver %q: Send with empty To got %v, want ErrNoRecipient", cfg.Driver, err)
		}
	}
}

// TestSMTPRequiresTLSUnlessExplicitlyOptedOut is the security assertion for the
// new dev-only opt-out: TLS is mandatory for the zero value, so neither an
// absent setting nor a false one can downgrade a production send to cleartext.
// Only an explicit true relaxes it (security Invariant 6).
func TestSMTPRequiresTLSUnlessExplicitlyOptedOut(t *testing.T) {
	base := Config{Driver: "smtp", SMTPHost: "smtp.test", SMTPPort: 587, From: "sys@acme.test"}

	if _, got := smtpPolicy(base); got != gomail.TLSMandatory {
		t.Errorf("default (AllowPlaintext unset): TLS policy = %v, want TLSMandatory", got)
	}

	explicitFalse := base
	explicitFalse.AllowPlaintext = false
	if _, got := smtpPolicy(explicitFalse); got != gomail.TLSMandatory {
		t.Errorf("AllowPlaintext=false: TLS policy = %v, want TLSMandatory", got)
	}

	optedOut := base
	optedOut.AllowPlaintext = true
	if _, got := smtpPolicy(optedOut); got != gomail.NoTLS {
		t.Errorf("AllowPlaintext=true: TLS policy = %v, want NoTLS", got)
	}
}

// TestSMTPOmitsAuthWithoutUsername covers the other half of talking to a local
// catcher: a server advertising no AUTH mechanism rejects PLAIN, so auth is
// offered only when a username is actually configured. This is independent of
// the TLS policy — omitting auth never relaxes transport security.
func TestSMTPOmitsAuthWithoutUsername(t *testing.T) {
	withUser := Config{Driver: "smtp", SMTPHost: "smtp.test", SMTPPort: 587, From: "sys@acme.test",
		SMTPUsername: "operator", SMTPPassword: "pw"}
	auth, tlsPolicy := smtpPolicy(withUser)
	if auth != gomail.SMTPAuthPlain {
		t.Errorf("with username: auth = %v, want %v", auth, gomail.SMTPAuthPlain)
	}
	if tlsPolicy != gomail.TLSMandatory {
		t.Errorf("with username: TLS policy = %v, want TLSMandatory", tlsPolicy)
	}

	noUser := Config{Driver: "smtp", SMTPHost: "smtp.test", SMTPPort: 1025, From: "sys@acme.test"}
	if auth, _ := smtpPolicy(noUser); auth != gomail.SMTPAuthNoAuth {
		t.Errorf("without username: auth = %v, want %v", auth, gomail.SMTPAuthNoAuth)
	}
}

// TestSMTPClientOptionsApplyThePolicy confirms the decision above is actually
// wired into the client that gets dialed — smtpPolicy could be correct while
// clientOptions dropped it on the floor.
func TestSMTPClientOptionsApplyThePolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want gomail.TLSPolicy
	}{
		{"secure default", Config{SMTPHost: "smtp.test", SMTPPort: 587}, gomail.TLSMandatory},
		{"explicit opt-out", Config{SMTPHost: "smtp.test", SMTPPort: 1025, AllowPlaintext: true}, gomail.NoTLS},
	} {
		s := &smtpSender{cfg: tc.cfg}
		c, err := gomail.NewClient(tc.cfg.SMTPHost, s.clientOptions()...)
		if err != nil {
			t.Fatalf("%s: NewClient: %v", tc.name, err)
		}
		if got := c.TLSPolicy(); got != tc.want.String() {
			t.Errorf("%s: client TLS policy = %q, want %q", tc.name, got, tc.want.String())
		}
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
