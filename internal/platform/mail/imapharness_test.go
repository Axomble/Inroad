package mail

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeHost is a TEST-NET-3 address: routable as far as vetAddr is concerned (so
// the guard genuinely runs and genuinely passes) and never actually dialed,
// because startFakeIMAP redirects the socket to the in-process listener. The
// pre-existing cancellation tests in net_tester_test.go use the same range for
// the same reason.
const fakeHost = "203.0.113.10"

func fakeIMAPConfig(user, pass string) IMAPConfig {
	return IMAPConfig{Host: fakeHost, Port: 993, Username: user, Password: pass}
}

// The harness has to be able to drive the code that exists before it is worth
// anything as evidence about the code that does not: a vanilla server that
// accepts the LOGIN command must still work end to end.
func TestFakeIMAPServerDrivesAFullFetch(t *testing.T) {
	srv := startFakeIMAP(t, imapScript{
		User: "user@example.com", Pass: "s3cret",
		UIDValidity: 42,
		Messages: []imapFakeMessage{
			{UID: 7, Raw: "From: a@example.com\nSubject: one\n\nbody one\n"},
			{UID: 9, Raw: "From: b@example.com\nSubject: two\n\nbody two\n"},
		},
	})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	msgs, uidValidity, err := r.Fetch(t.Context(), fakeIMAPConfig("user@example.com", "s3cret"), 0, 10)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if uidValidity != 42 {
		t.Errorf("uidValidity = %d, want 42", uidValidity)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].UID != 7 || msgs[1].UID != 9 {
		t.Errorf("UIDs = %d,%d; want 7,9", msgs[0].UID, msgs[1].UID)
	}
	if got := msgs[0].Header.Get("Subject"); got != "one" {
		t.Errorf("subject = %q, want %q", got, "one")
	}
	if !srv.sawAuthenticated() {
		t.Error("server never saw a successful authentication")
	}
	// A vanilla server (no AUTH= capability advertised) must still be driven with
	// the plain LOGIN command — the path every currently-working mailbox uses.
	if !containsCommand(srv.commandLog(), "LOGIN ") {
		t.Errorf("no LOGIN command reached the server; commands were %v", srv.commandLog())
	}
}

// containsCommand reports whether any logged command line contains want (the
// client's tag is part of each line, so callers match on the verb).
func containsCommand(log []string, want string) bool {
	for _, line := range log {
		if strings.Contains(strings.ToUpper(line), strings.ToUpper(want)) {
			return true
		}
	}
	return false
}

// CurrentState is the poll path's baseline call, and it takes a different route
// through selectInboxReadOnly than Fetch does.
func TestFakeIMAPServerDrivesCurrentState(t *testing.T) {
	startFakeIMAP(t, imapScript{User: "u", Pass: "p", UIDValidity: 9, UIDNext: 501})

	r := &NetInboxReader{Timeout: 5 * time.Second}
	uidValidity, uidNext, err := r.CurrentState(t.Context(), fakeIMAPConfig("u", "p"))
	if err != nil {
		t.Fatalf("CurrentState: %v", err)
	}
	if uidValidity != 9 || uidNext != 501 {
		t.Errorf("got (%d, %d), want (9, 501)", uidValidity, uidNext)
	}
}

// The transport seam must be exactly that — a socket, not a policy. A host the
// SSRF guard rejects must still be rejected with the seam installed, or every
// test written against this harness would be evidence about a code path
// production never takes.
//
// Removing the vetAddr call from selectInboxReadOnly makes this test fail and
// every other test in this file still pass, which is the asymmetry it exists for.
func TestTransportSeamDoesNotBypassTheSSRFGuard(t *testing.T) {
	startFakeIMAP(t, imapScript{User: "u", Pass: "p"})
	r := &NetInboxReader{Timeout: 5 * time.Second} // AllowPrivate false

	for _, tc := range []struct {
		name string
		cfg  IMAPConfig
	}{
		{"loopback", IMAPConfig{Host: "127.0.0.1", Port: 993, Username: "u", Password: "p"}},
		{"cloud metadata", IMAPConfig{Host: "169.254.169.254", Port: 993, Username: "u", Password: "p"}},
		{"private range", IMAPConfig{Host: "10.0.0.5", Port: 993, Username: "u", Password: "p"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := r.CurrentState(t.Context(), tc.cfg); !errors.Is(err, ErrHostNotPermitted) {
				t.Fatalf("got %v, want ErrHostNotPermitted", err)
			}
		})
	}

	t.Run("disallowed port", func(t *testing.T) {
		cfg := IMAPConfig{Host: fakeHost, Port: 6379, Username: "u", Password: "p"}
		if _, _, err := r.CurrentState(t.Context(), cfg); err == nil {
			t.Fatal("a non-IMAP port reached the transport")
		}
	})
}

// A server that completes the TCP handshake and then says nothing is the failure
// a dial timeout does not cover. go-imap's Client.Timeout is what bounds it, and
// dialIMAP sets it before the greeting is read.
func TestIMAPServerThatAcceptsThenGoesSilentIsBounded(t *testing.T) {
	startFakeIMAP(t, imapScript{SilentAfterGreeting: true})

	r := &NetInboxReader{Timeout: 750 * time.Millisecond}
	start := time.Now()
	_, _, err := r.CurrentState(t.Context(), fakeIMAPConfig("u", "p"))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a silent server returned no error")
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v to give up on a silent server with a 750ms timeout", elapsed)
	}
	if strings.Contains(err.Error(), "p") && strings.Contains(err.Error(), "password") {
		t.Errorf("error text looks like it carries the credential: %v", err)
	}
}
