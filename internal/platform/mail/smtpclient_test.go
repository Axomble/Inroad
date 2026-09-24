package mail

import (
	"strings"
	"testing"
	"time"

	gomail "github.com/wneessen/go-mail"
)

func fakeSMTPConfig(user, pass string) SMTPConfig {
	return SMTPConfig{
		Host: fakeHost, Port: 587,
		Username: user, Password: pass,
		// The scripted servers speak cleartext; see smtpfake_test.go's note.
		AllowPlaintext: true,
	}
}

func testMessage() Message {
	return Message{
		FromEmail: "rep@sales.example.com", FromName: "Rep",
		To: "lead@example.net", Subject: "hello", BodyText: "hi",
	}
}

// EHLO identifies the sending host, and receivers judge it: Postfix's
// reject_non_fqdn_helo_hostname and most commercial filters penalise or refuse a
// greeting that is not a fully qualified name. go-mail defaults to os.Hostname(),
// which inside a container is a random hex id — so the same message that passes
// from a developer's laptop is filtered from a deployment.
func TestSMTPSendGreetsWithTheSenderDomain(t *testing.T) {
	srv := startFakeSMTP(t, smtpScript{Extensions: []string{"AUTH PLAIN"}, User: "u", Pass: "p"})

	s := &NetSender{Timeout: 5 * time.Second}
	if _, err := s.Send(t.Context(), fakeSMTPConfig("u", "p"), testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := srv.heloName(); got != "sales.example.com" {
		t.Errorf("EHLO %q, want the sender domain %q", got, "sales.example.com")
	}
}

// The connection test must greet as the same host the send will, or a mailbox
// can pass its connection test and then have every message refused.
func TestSMTPConnectionTestGreetsWithTheMailboxDomain(t *testing.T) {
	srv := startFakeSMTP(t, smtpScript{Extensions: []string{"AUTH PLAIN"}, User: "u", Pass: "p"})

	cfg := fakeSMTPConfig("u", "p")
	cfg.EHLODomain = "sales.example.com"
	tester := &NetTester{Timeout: 5 * time.Second}
	if err := tester.TestSMTP(t.Context(), cfg); err != nil {
		t.Fatalf("TestSMTP: %v", err)
	}
	if got := srv.heloName(); got != "sales.example.com" {
		t.Errorf("EHLO %q, want %q", got, "sales.example.com")
	}
}

// A server that offers only AUTH LOGIN — Exchange front ends, several shared
// hosts, and most cPanel installs. WithSMTPAuth(SMTPAuthPlain) fails these with
// "server does not support SMTP AUTH type: PLAIN" on every single send.
func TestSMTPSendNegotiatesAuthLogin(t *testing.T) {
	srv := startFakeSMTP(t, smtpScript{Extensions: []string{"AUTH LOGIN"}, User: "u", Pass: "p"})

	s := &NetSender{Timeout: 5 * time.Second}
	if _, err := s.Send(t.Context(), fakeSMTPConfig("u", "p"), testMessage()); err != nil {
		t.Fatalf("Send against an AUTH LOGIN server: %v", err)
	}
	if got := srv.mechanism(); got != "LOGIN" {
		t.Errorf("mechanism %q, want LOGIN", got)
	}
	if !srv.authenticated() {
		t.Error("the server never saw a successful AUTH")
	}
}

// The same server shape through the connection test, which had its own,
// different auth path (stdlib smtp.PlainAuth) and so could disagree with the
// sender about whether a mailbox works.
func TestSMTPConnectionTestNegotiatesAuthLogin(t *testing.T) {
	srv := startFakeSMTP(t, smtpScript{Extensions: []string{"AUTH LOGIN"}, User: "u", Pass: "p"})

	tester := &NetTester{Timeout: 5 * time.Second}
	if err := tester.TestSMTP(t.Context(), fakeSMTPConfig("u", "p")); err != nil {
		t.Fatalf("TestSMTP against an AUTH LOGIN server: %v", err)
	}
	if got := srv.mechanism(); got != "LOGIN" {
		t.Errorf("mechanism %q, want LOGIN", got)
	}
}

// A server offering only CRAM-MD5.
func TestSMTPSendNegotiatesCramMD5(t *testing.T) {
	srv := startFakeSMTP(t, smtpScript{Extensions: []string{"AUTH CRAM-MD5"}, User: "u", Pass: "p"})

	s := &NetSender{Timeout: 5 * time.Second}
	if _, err := s.Send(t.Context(), fakeSMTPConfig("u", "p"), testMessage()); err != nil {
		t.Fatalf("Send against a CRAM-MD5 server: %v", err)
	}
	if got := srv.mechanism(); got != "CRAM-MD5" {
		t.Errorf("mechanism %q, want CRAM-MD5", got)
	}
}

// A relay that requires no authentication at all. The connection test skipped
// AUTH when the username was empty; the sender always ran it, so this mailbox
// tested clean and then failed every send with "server does not support SMTP
// AUTH" — the two halves disagreeing about the same mailbox.
func TestSMTPUnauthenticatedRelaySendsAndTestsAlike(t *testing.T) {
	srv := startFakeSMTP(t, smtpScript{})

	s := &NetSender{Timeout: 5 * time.Second}
	if _, err := s.Send(t.Context(), fakeSMTPConfig("", ""), testMessage()); err != nil {
		t.Fatalf("Send to an unauthenticated relay: %v", err)
	}
	if srv.sawCommand("AUTH") {
		t.Errorf("attempted AUTH %q against a relay that offers none", srv.mechanism())
	}
	if !strings.Contains(srv.message(), "Subject: hello") {
		t.Errorf("the message never arrived: %q", srv.message())
	}

	tester := &NetTester{Timeout: 5 * time.Second}
	if err := tester.TestSMTP(t.Context(), fakeSMTPConfig("", "")); err != nil {
		t.Fatalf("TestSMTP against an unauthenticated relay: %v", err)
	}
}

// The SMTP counterpart of TestIMAPServerThatAcceptsThenGoesSilentIsBounded: a
// server that completes the TCP handshake and then never greets. go-mail sets a
// conn deadline immediately after dialing and refreshes it per phase, so both
// the send and the connection test are already bounded here — this test exists
// to keep that true, and to make the guarantee explicit for the path that runs
// on an HTTP request. Unlike the IMAP side, it found no bug.
func TestSMTPServerThatAcceptsThenGoesSilentIsBounded(t *testing.T) {
	startFakeSMTP(t, smtpScript{SilentAfterConnect: true})

	t.Run("send", func(t *testing.T) {
		s := &NetSender{Timeout: 750 * time.Millisecond}
		start := time.Now()
		_, err := s.Send(t.Context(), fakeSMTPConfig("u", "p"), testMessage())
		if err == nil {
			t.Fatal("a silent server returned no error")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("took %v against a 750ms timeout", elapsed)
		}
	})

	t.Run("connection test", func(t *testing.T) {
		tester := &NetTester{Timeout: 750 * time.Millisecond}
		start := time.Now()
		err := tester.TestSMTP(t.Context(), fakeSMTPConfig("u", "p"))
		if err == nil {
			t.Fatal("a silent server returned no error")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("took %v against a 750ms timeout", elapsed)
		}
	})
}

// heloDomain is the pure half, including the part that matters most: EHLO's
// argument comes from a workspace-controlled address, and go-mail passes it to
// the wire verbatim. A name carrying CRLF would be SMTP command injection, so
// anything that is not a plausible FQDN yields "" and the caller keeps go-mail's
// default rather than sending it.
func TestHeloDomain(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"rep@sales.example.com", "sales.example.com"},
		{"Rep <rep@Example.COM>", "example.com"},
		{"rep@example.com.", "example.com"},
		{"rep@example.com\r\nQUIT", ""},
		{"rep@example.com\nMAIL FROM:<x@y.z>", ""},
		{"rep@localhost", ""},
		{"rep@", ""},
		{"@example.com", "example.com"},
		{"not-an-address", ""},
		{"", ""},
		// Bare domains, the form SMTPConfig.EHLODomain carries.
		{"sales.example.com", "sales.example.com"},
		{"Sales.Example.Com.", "sales.example.com"},
		{"sales.example.com\r\nQUIT", ""},
		{"rep@-bad.example.com", ""},
		{"rep@exa mple.com", ""},
		{"rep@" + strings.Repeat("a", 64) + ".com", ""},
	} {
		if got := heloDomain(tc.in); got != tc.want {
			t.Errorf("heloDomain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The mechanism preference order, and the one place the cleartext opt-out
// changes it: go-mail's PLAIN and LOGIN refuse to put a credential on an
// unencrypted connection unless asked in so many words, so a mailbox that has
// explicitly opted into cleartext needs the -NOENC variants or it cannot
// authenticate at all. TLS-enforced mailboxes (the default, and everything that
// has not opted out) never see them.
func TestSMTPAuthMechanismOrder(t *testing.T) {
	encrypted := smtpAuthMechanisms(SMTPConfig{})
	want := []gomail.SMTPAuthType{gomail.SMTPAuthCramMD5, gomail.SMTPAuthLogin, gomail.SMTPAuthPlain}
	if len(encrypted) != len(want) {
		t.Fatalf("got %v, want %v", encrypted, want)
	}
	for i := range want {
		if encrypted[i] != want[i] {
			t.Errorf("position %d = %q, want %q", i, encrypted[i], want[i])
		}
	}

	cleartext := smtpAuthMechanisms(SMTPConfig{AllowPlaintext: true})
	if cleartext[0] != gomail.SMTPAuthCramMD5 {
		t.Errorf("cleartext order should still lead with CRAM-MD5, got %q", cleartext[0])
	}
	for _, m := range cleartext[1:] {
		if !strings.HasSuffix(string(m), "-NOENC") {
			t.Errorf("cleartext mechanism %q should be the -NOENC variant", m)
		}
	}
}
