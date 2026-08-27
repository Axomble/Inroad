package wsticket

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/platform/track"
)

var testSecret = []byte("test-secret-key-at-least-16")

// A fixed clock, so an expiry assertion never depends on how long the test took.
var now = time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

func validTicket() Ticket {
	return Ticket{
		WorkspaceID: "8f14e45f-ceea-467e-adc1-0000000000ab",
		UserID:      "3c59dc04-8e88-4e53-a8f4-0000000000cd",
		SessionID:   "b6d767d2-f8ed-4f57-a8ff-0000000000ef",
		ExpiresAt:   now.Add(DefaultTTL),
		Nonce:       "Zm9vYmFyYmF6cXV1eA",
	}
}

func TestTicket_RoundTrip(t *testing.T) {
	want := validTicket()

	got, err := Parse(testSecret, Make(testSecret, want), now)
	if err != nil {
		t.Fatalf("Parse() err = %v, want nil", err)
	}

	if got.WorkspaceID != want.WorkspaceID {
		t.Errorf("WorkspaceID = %q, want %q", got.WorkspaceID, want.WorkspaceID)
	}
	if got.UserID != want.UserID {
		t.Errorf("UserID = %q, want %q", got.UserID, want.UserID)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, want.SessionID)
	}
	if got.Nonce != want.Nonce {
		t.Errorf("Nonce = %q, want %q", got.Nonce, want.Nonce)
	}
	// Unix-second granularity is all the payload carries, so compare on that.
	if !got.ExpiresAt.Equal(want.ExpiresAt.Truncate(time.Second)) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want.ExpiresAt.Truncate(time.Second))
	}
}

func TestParse_TamperedSignature(t *testing.T) {
	token := Make(testSecret, validTicket())

	// Flip the last character of the signature half.
	dot := strings.IndexByte(token, '.')
	sig := []byte(token[dot+1:])
	if sig[len(sig)-1] == 'A' {
		sig[len(sig)-1] = 'B'
	} else {
		sig[len(sig)-1] = 'A'
	}

	if _, err := Parse(testSecret, token[:dot+1]+string(sig), now); !errors.Is(err, ErrSignature) {
		t.Errorf("Parse() err = %v, want ErrSignature", err)
	}
}

func TestParse_WrongSecret(t *testing.T) {
	token := Make(testSecret, validTicket())

	if _, err := Parse([]byte("a-completely-different-secret"), token, now); !errors.Is(err, ErrSignature) {
		t.Errorf("Parse() err = %v, want ErrSignature", err)
	}
}

// The workspace is the tenant boundary: a client that could re-sign a payload
// could read another workspace's whole event stream.
func TestParse_TamperedWorkspaceIsRefused(t *testing.T) {
	tk := validTicket()
	forged := payloadOf(tk)
	forged = strings.Replace(forged, tk.WorkspaceID, "00000000-0000-0000-0000-00000000dead", 1)

	// Keep the ORIGINAL signature, swap the payload — the attack an attacker can
	// actually mount without the secret.
	original := Make(testSecret, tk)
	token := base64.RawURLEncoding.EncodeToString([]byte(forged)) + "." + original[strings.IndexByte(original, '.')+1:]

	if _, err := Parse(testSecret, token, now); !errors.Is(err, ErrSignature) {
		t.Errorf("Parse() err = %v, want ErrSignature", err)
	}
}

func TestParse_Expired(t *testing.T) {
	tk := validTicket()
	tk.ExpiresAt = now.Add(-time.Second)

	if _, err := Parse(testSecret, Make(testSecret, tk), now); !errors.Is(err, ErrExpired) {
		t.Errorf("Parse() err = %v, want ErrExpired", err)
	}
}

// The boundary is exclusive: a ticket is dead ON its expiry second. Asserting
// both sides of the same instant is what pins the comparison direction — a `<=`
// where a `<` belongs would pass a one-sided test.
func TestParse_ExpiryBoundaryIsExclusive(t *testing.T) {
	tk := validTicket()
	tk.ExpiresAt = now.Add(DefaultTTL)
	token := Make(testSecret, tk)

	if _, err := Parse(testSecret, token, tk.ExpiresAt.Add(-time.Nanosecond)); err != nil {
		t.Errorf("one instant before expiry: err = %v, want nil", err)
	}
	if _, err := Parse(testSecret, token, tk.ExpiresAt); !errors.Is(err, ErrExpired) {
		t.Errorf("exactly at expiry: err = %v, want ErrExpired", err)
	}
}

// Domain separation across PACKAGES, not just within one. All of these codecs
// fall back to the same JWTSecret when a dedicated secret is unset, so a
// tracking token reaching the handshake must not authenticate anyone.
func TestParse_RejectsTrackingToken(t *testing.T) {
	openToken := track.MakeOpenToken(testSecret, "8f14e45f-ceea-467e-adc1-0000000000ab")

	if _, err := Parse(testSecret, openToken, now); !errors.Is(err, ErrMalformed) {
		t.Errorf("Parse(open token) err = %v, want ErrMalformed", err)
	}
}

// The test above is NOT sufficient on its own, and this one says why: a track
// token carries one field, so it is refused by the field COUNT even with the
// domain guard deleted. That makes it an accidental pass, not a test of domain
// separation.
//
// This is the payload that isolates the guard — a foreign domain prefix over
// wsticket's exact five-field shape, correctly signed with the shared secret.
// It is what another same-secret codec would produce if it ever signed
// colon-separated fields, and the ONLY thing standing between it and an
// authenticated socket is the prefix check.
//
// Verified by deleting that check: this test fails, every other test in the file
// still passes.
func TestParse_RejectsForeignDomainWithValidShape(t *testing.T) {
	tk := validTicket()
	sep := string(fieldSep)
	// "o:" is track's open-token domain; everything after it is a valid ticket.
	payload := "o:" + tk.WorkspaceID + sep + tk.UserID + sep + tk.SessionID + sep +
		strconv.FormatInt(tk.ExpiresAt.Unix(), 10) + sep + tk.Nonce

	token := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sign(testSecret, payload))

	got, err := Parse(testSecret, token, now)
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("Parse() err = %v, want ErrMalformed", err)
	}
	// Belt and braces: nothing may leak out of a refused ticket. A parser that
	// returned the workspace alongside an error would hand a caller that ignored
	// the error a cross-tenant channel key.
	if got.WorkspaceID != "" {
		t.Errorf("WorkspaceID = %q on a refused ticket, want empty", got.WorkspaceID)
	}
}

// ...and the reverse, so neither codec can be fed the other's output.
func TestTrackingParse_RejectsConnectTicket(t *testing.T) {
	token := Make(testSecret, validTicket())

	if _, ok := track.ParseOpenToken(testSecret, token); ok {
		t.Error("track.ParseOpenToken() accepted a connect ticket, want rejected")
	}
	if _, _, ok := track.ParseClickToken(testSecret, token); ok {
		t.Error("track.ParseClickToken() accepted a connect ticket, want rejected")
	}
}

// Each field gates a distinct control: workspace pins the tenant, session powers
// the logout re-check, nonce makes the ticket single-use. A blank one must not
// parse, or the control it gates is silently skipped.
func TestParse_RejectsEmptyFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Ticket)
	}{
		{"workspace", func(tk *Ticket) { tk.WorkspaceID = "" }},
		{"user", func(tk *Ticket) { tk.UserID = "" }},
		{"session", func(tk *Ticket) { tk.SessionID = "" }},
		{"nonce", func(tk *Ticket) { tk.Nonce = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tk := validTicket()
			tc.mutate(&tk)

			// Signed with the real secret: this is a well-formed ticket the SERVER
			// could mint by mistake, not a forgery. It must still be refused.
			if _, err := Parse(testSecret, Make(testSecret, tk), now); !errors.Is(err, ErrMalformed) {
				t.Errorf("Parse() err = %v, want ErrMalformed", err)
			}
		})
	}
}

// A field carrying the separator must not shift the other fields along. Callers
// pass UUIDs and base64, so this cannot happen today — but the parser does not
// get to assume that about a payload whose shape an attacker may have chosen.
func TestParse_FieldContainingSeparatorDoesNotShiftFields(t *testing.T) {
	tk := validTicket()
	tk.Nonce = "abc:def:ghi"

	got, err := Parse(testSecret, Make(testSecret, tk), now)
	if err != nil {
		t.Fatalf("Parse() err = %v, want nil", err)
	}
	// SplitN keeps the tail intact, so the nonce survives whole and — crucially —
	// the session id is still the session id.
	if got.Nonce != tk.Nonce {
		t.Errorf("Nonce = %q, want %q", got.Nonce, tk.Nonce)
	}
	if got.SessionID != tk.SessionID {
		t.Errorf("SessionID = %q, want %q — fields shifted", got.SessionID, tk.SessionID)
	}
}

func TestParse_GarbageInput(t *testing.T) {
	for _, token := range []string{
		"",
		".",
		"no-dot-at-all",
		"!!!not-base64!!!.!!!nope!!!",
		Make(testSecret, validTicket()) + "trailing",
	} {
		if _, err := Parse(testSecret, token, now); err == nil {
			t.Errorf("Parse(%q) err = nil, want an error", token)
		}
	}
}

// A payload that verifies but carries a non-numeric expiry is malformed, not
// expired — the distinction keeps the handshake's logs honest.
func TestParse_NonNumericExpiry(t *testing.T) {
	payload := ticketPrefix + "ws:user:session:not-a-number:nonce"
	token := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sign(testSecret, payload))

	if _, err := Parse(testSecret, token, now); !errors.Is(err, ErrMalformed) {
		t.Errorf("Parse() err = %v, want ErrMalformed", err)
	}
}

// Two tickets minted for the same session must still differ, or burning one
// nonce would invalidate every concurrent tab.
func TestMake_DistinctNoncesProduceDistinctTickets(t *testing.T) {
	a := validTicket()
	b := validTicket()
	b.Nonce = "ZGlmZmVyZW50bm9uY2U"

	if Make(testSecret, a) == Make(testSecret, b) {
		t.Error("tickets differing only by nonce produced identical tokens")
	}
}
