package mail

import (
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // RFC 2195 defines CRAM-MD5 in terms of HMAC-MD5; the test vector is the spec's.
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// Each test here is a server shape that exists in the wild and that the
// unconditional c.Login() call could not authenticate to. They drive the real
// reader through the real SSRF-vetted dial path (startFakeIMAP swaps the socket
// only), so what they assert is the conversation, not a helper's return value.

// The commonest one: an RFC 3501 server that disables the LOGIN command and
// offers SASL instead. c.Login() returns go-imap's ErrLoginDisabled without
// sending anything, so the mailbox connects, tests green, and then never polls.
func TestIMAPAuthNegotiatesWhenTheLoginCommandIsDisabled(t *testing.T) {
	srv := startFakeIMAP(t, imapScript{
		Caps: []string{"LOGINDISABLED", "AUTH=PLAIN", "SASL-IR"},
		User: "u@example.com", Pass: "s3cret",
		UIDValidity: 3, UIDNext: 11,
	})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	uidValidity, uidNext, err := r.CurrentState(t.Context(), fakeIMAPConfig("u@example.com", "s3cret"))
	if err != nil {
		t.Fatalf("CurrentState against a LOGINDISABLED server: %v", err)
	}
	if uidValidity != 3 || uidNext != 11 {
		t.Errorf("got (%d, %d), want (3, 11)", uidValidity, uidNext)
	}
	if !srv.sawAuthenticated() {
		t.Fatal("server never saw a successful authentication")
	}
	if containsCommand(srv.commandLog(), "LOGIN \"") {
		t.Errorf("sent the LOGIN command to a LOGINDISABLED server: %v", srv.commandLog())
	}
}

// A server that offers ONLY CRAM-MD5 — no PLAIN, no LOGIN command. go-sasl does
// not ship a CRAM-MD5 client at the version in this module graph, so this is
// also the test that the hand-written mechanism computes RFC 2195 correctly
// against an independent implementation (the fake server's).
func TestIMAPAuthUsesCramMD5WhenItIsTheOnlyMechanism(t *testing.T) {
	srv := startFakeIMAP(t, imapScript{
		Caps: []string{"LOGINDISABLED", "AUTH=CRAM-MD5"},
		User: "u@example.com", Pass: "s3cret",
		UIDValidity: 5, UIDNext: 2,
	})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	if _, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u@example.com", "s3cret")); err != nil {
		t.Fatalf("CurrentState against a CRAM-MD5-only server: %v", err)
	}
	if !srv.sawAuthenticated() {
		t.Fatal("CRAM-MD5 negotiation did not authenticate")
	}
	if !containsCommand(srv.commandLog(), "AUTHENTICATE CRAM-MD5") {
		t.Errorf("no AUTHENTICATE CRAM-MD5 reached the server: %v", srv.commandLog())
	}
}

// AUTH=LOGIN with no SASL-IR, where the server opens with the base64 "Username:"
// prompt that Dovecot, Courier and Exchange all send. go-sasl's own LOGIN client
// rejects that challenge outright (it only accepts "Password:", expecting the
// username to have gone out as an initial response), so using it here would fail
// against the servers AUTH=LOGIN exists for.
func TestIMAPAuthHandlesTheUsernamePromptFormOfAuthLogin(t *testing.T) {
	srv := startFakeIMAP(t, imapScript{
		Caps: []string{"LOGINDISABLED", "AUTH=LOGIN"},
		User: "u@example.com", Pass: "s3cret",
		UsernameChallenge: true,
	})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	if _, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u@example.com", "s3cret")); err != nil {
		t.Fatalf("CurrentState against a Username:-prompting AUTH=LOGIN server: %v", err)
	}
	if !srv.sawAuthenticated() {
		t.Fatal("AUTH=LOGIN negotiation did not authenticate")
	}
}

// The same mechanism where the server DOES advertise SASL-IR, so the username
// travels on the command line and only the password is prompted for. The client
// has to cope with both shapes from one implementation.
func TestIMAPAuthHandlesAuthLoginWithInitialResponse(t *testing.T) {
	srv := startFakeIMAP(t, imapScript{
		Caps: []string{"LOGINDISABLED", "AUTH=LOGIN", "SASL-IR"},
		User: "u@example.com", Pass: "s3cret",
	})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	if _, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u@example.com", "s3cret")); err != nil {
		t.Fatalf("CurrentState against a SASL-IR AUTH=LOGIN server: %v", err)
	}
	if !srv.sawAuthenticated() {
		t.Fatal("AUTH=LOGIN with an initial response did not authenticate")
	}
}

// A server that advertises no LOGINDISABLED but refuses the LOGIN command
// anyway — a real and non-compliant shape (policy-restricted relays, some
// Exchange front ends). One fallback to the LOGIN command in the other
// direction is what protects every mailbox that works today; this is the same
// guarantee from the other side.
func TestIMAPAuthFallsBackToSASLWhenTheLoginCommandIsRefused(t *testing.T) {
	srv := startFakeIMAP(t, imapScript{
		Caps: []string{"AUTH=PLAIN", "SASL-IR"},
		User: "u@example.com", Pass: "s3cret",
		LoginCommandReply: "NO [PRIVACYREQUIRED] plaintext LOGIN is not available",
	})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	if _, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u@example.com", "s3cret")); err != nil {
		t.Fatalf("CurrentState against a LOGIN-refusing server: %v", err)
	}
	if !srv.sawAuthenticated() {
		t.Fatal("no fallback mechanism authenticated")
	}
}

// The other direction, and the one with regression risk: a server that
// advertises a SASL mechanism its accounts cannot actually use must still reach
// the LOGIN command that works today. Without the fallback, turning on
// negotiation would BREAK working mailboxes, which is the opposite of P1.8.
func TestIMAPAuthFallsBackToTheLoginCommandWhenTheMechanismFails(t *testing.T) {
	srv := startFakeIMAP(t, imapScript{
		// Advertised globally, unusable for this account — what a server whose
		// password storage is hashed-only does with a mechanism that needs the
		// plaintext-equivalent secret.
		Caps:             []string{"AUTH=CRAM-MD5"},
		RefuseMechanisms: []string{"CRAM-MD5"},
		User:             "u@example.com", Pass: "s3cret",
	})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	if _, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u@example.com", "s3cret")); err != nil {
		t.Fatalf("CurrentState did not fall back to LOGIN: %v", err)
	}
	if !srv.sawAuthenticated() {
		t.Fatal("neither the mechanism nor the fallback authenticated")
	}
	if !containsCommand(srv.commandLog(), "AUTHENTICATE CRAM-MD5") {
		t.Errorf("the mechanism was never attempted: %v", srv.commandLog())
	}
	if !containsCommand(srv.commandLog(), "LOGIN \"") {
		t.Errorf("the LOGIN fallback never ran: %v", srv.commandLog())
	}
}

// Preference order among advertised mechanisms.
func TestIMAPAuthPrefersCramMD5OverLoginOverPlain(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps []string
		want string
	}{
		{"all three", []string{"AUTH=PLAIN", "AUTH=LOGIN", "AUTH=CRAM-MD5"}, "AUTHENTICATE CRAM-MD5"},
		{"login and plain", []string{"AUTH=PLAIN", "AUTH=LOGIN"}, "AUTHENTICATE LOGIN"},
		{"plain only", []string{"AUTH=PLAIN"}, "AUTHENTICATE PLAIN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startFakeIMAP(t, imapScript{
				Caps: append(tc.caps, "LOGINDISABLED"),
				User: "u@example.com", Pass: "s3cret",
			})
			r := &NetInboxReader{Timeout: 5 * time.Second}
			if _, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u@example.com", "s3cret")); err != nil {
				t.Fatalf("CurrentState: %v", err)
			}
			if !containsCommand(srv.commandLog(), tc.want) {
				t.Errorf("wanted %q in %v", tc.want, srv.commandLog())
			}
		})
	}
}

// A server that advertises nothing usable and disables LOGIN has to produce an
// error an operator can act on, not a silent no-op or a nil-credential dial.
func TestIMAPAuthReportsWhenNoMechanismIsUsable(t *testing.T) {
	startFakeIMAP(t, imapScript{
		Caps: []string{"LOGINDISABLED", "AUTH=GSSAPI"},
		User: "u@example.com", Pass: "s3cret",
	})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	_, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u@example.com", "s3cret"))
	if err == nil {
		t.Fatal("expected an error when no mechanism is usable")
	}
	if !strings.Contains(err.Error(), "authentication") {
		t.Errorf("error does not name the problem: %v", err)
	}
}

// docs/security.md invariant: a credential never reaches a log or an error
// string. A failed negotiation is the case where it would be most tempting to
// include one, and where the whole exchange (challenge, digest, password) is in
// scope.
func TestIMAPAuthFailureNeverCarriesTheCredential(t *testing.T) {
	const password = "correct-horse-battery-staple"
	for _, caps := range [][]string{
		{"LOGINDISABLED", "AUTH=CRAM-MD5"},
		{"LOGINDISABLED", "AUTH=PLAIN", "SASL-IR"},
		{"LOGINDISABLED", "AUTH=LOGIN"},
		nil,
	} {
		startFakeIMAP(t, imapScript{Caps: caps, User: "u@example.com", Pass: "a-different-secret"})
		r := &NetInboxReader{Timeout: 5 * time.Second}
		_, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u@example.com", password))
		if err == nil {
			t.Fatalf("caps %v: expected an authentication failure", caps)
		}
		if strings.Contains(err.Error(), password) {
			t.Errorf("caps %v: error text contains the password: %v", caps, err)
		}
	}
}

// RFC 2195's own worked example, so the mechanism is checked against the
// specification rather than only against this repo's fake server.
func TestCramMD5ClientMatchesTheRFC2195Example(t *testing.T) {
	const (
		challenge = "<1896.697170952@postoffice.reston.mci.net>"
		username  = "tim"
		password  = "tanstaaftanstaaf"
		want      = "tim b913a602c7eda7a495b4e6e7334d3890"
	)
	c := newCramMD5Client(username, password)
	mech, ir, err := c.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if mech != "CRAM-MD5" {
		t.Errorf("mechanism = %q, want CRAM-MD5", mech)
	}
	if ir != nil {
		t.Errorf("CRAM-MD5 has no initial response, got %q", ir)
	}
	got, err := c.Next([]byte(challenge))
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(got) != want {
		t.Errorf("response = %q, want %q", got, want)
	}

	// Belt and braces: the same digest computed independently here, so a typo in
	// the constant above cannot make a broken implementation look correct.
	mac := hmac.New(md5.New, []byte(password))
	mac.Write([]byte(challenge))
	if expect := username + " " + hex.EncodeToString(mac.Sum(nil)); expect != want {
		t.Fatalf("the RFC constant and HMAC-MD5 disagree: %q vs %q", expect, want)
	}
}

// One challenge, one response: a server that keeps prompting is not a CRAM-MD5
// server, and replaying the digest at it is not a useful thing to do.
func TestCramMD5ClientRefusesASecondChallenge(t *testing.T) {
	c := newCramMD5Client("tim", "tanstaaftanstaaf")
	if _, err := c.Next([]byte("<a@b>")); err != nil {
		t.Fatalf("first challenge: %v", err)
	}
	if _, err := c.Next([]byte("<c@d>")); err == nil {
		t.Error("expected an error on a second CRAM-MD5 challenge")
	}
}

func TestCramMD5ClientRefusesAnEmptyChallenge(t *testing.T) {
	c := newCramMD5Client("tim", "tanstaaftanstaaf")
	if _, err := c.Next(nil); err == nil {
		t.Error("expected an error on an empty CRAM-MD5 challenge")
	}
}

// The LOGIN mechanism's two prompt shapes, at the unit level, so a regression in
// prompt handling names the cause rather than showing up as a connection failure.
func TestLoginMechanismAnswersBothPromptShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prompts []string
		want    []string
	}{
		{"username prompt then password", []string{"Username:", "Password:"}, []string{"u", "p"}},
		{"empty prompt then password", []string{"", "Password:"}, []string{"u", "p"}},
		{"lowercase prompts", []string{"username", "password"}, []string{"u", "p"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newIMAPLoginClient("u", "p")
			mech, ir, err := c.Start()
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if mech != "LOGIN" || string(ir) != "u" {
				t.Fatalf("Start = (%q, %q), want (LOGIN, u)", mech, ir)
			}
			for i, prompt := range tc.prompts {
				got, err := c.Next([]byte(prompt))
				if err != nil {
					t.Fatalf("prompt %q: %v", prompt, err)
				}
				if string(got) != tc.want[i] {
					t.Errorf("prompt %q -> %q, want %q", prompt, got, tc.want[i])
				}
			}
		})
	}
}
