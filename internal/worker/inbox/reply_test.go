package inbox

import (
	"strings"
	"testing"
)

func TestMessageIDsExtractsInReplyToThenReferences(t *testing.T) {
	h := hdrOnly(t, `From: bob@example.com
To: alice@example.com
Subject: Re: Hello
In-Reply-To: <a@x>
References: <b@y> <a@x>

Sounds good.
`)
	got := MessageIDs(h)
	want := []string{"<a@x>", "<b@y>", "<a@x>"}
	if !equalStrings(got, want) {
		t.Fatalf("MessageIDs = %v, want %v", got, want)
	}
}

func TestMessageIDsAbsentReturnsEmpty(t *testing.T) {
	h := hdrOnly(t, `From: bob@example.com
To: alice@example.com
Subject: Hello

Hi there.
`)
	got := MessageIDs(h)
	if len(got) != 0 {
		t.Fatalf("MessageIDs = %v, want empty", got)
	}
}

func TestMessageIDsIgnoresTokensWithoutAngleBrackets(t *testing.T) {
	h := hdrOnly(t, `From: bob@example.com
To: alice@example.com
Subject: Re: Hello
References: not-a-message-id <a@x> also-not-one

Hi.
`)
	got := MessageIDs(h)
	want := []string{"<a@x>"}
	if !equalStrings(got, want) {
		t.Fatalf("MessageIDs = %v, want %v", got, want)
	}
}

// A Message-ID is a lookup KEY on its way into a Postgres text parameter, and it
// comes off unauthenticated mail. textproto.ReadMIMEHeader validates header keys
// only, so a raw 0xFF or NUL in the VALUE survives verbatim — and Postgres
// refuses such a parameter with SQLSTATE 22021, an error that is neither
// pgx.ErrNoRows nor transient. The poll would then return before SetInboxCursor
// and the mailbox would be refetching the same message forever.
//
// Dropping the token costs nothing: RFC 5322's msg-id is dot-atom "@" dot-atom,
// US-ASCII by definition, so a byte outside printable ASCII is already not an
// identifier we could have issued.
func TestMessageIDsDropsTokensThatArePrintableASCII(t *testing.T) {
	h := hdrOnly(t, "From: bob@example.com\nTo: alice@example.com\nSubject: Re: Hello\n"+
		"In-Reply-To: <\xffbad@x>\nReferences: <\x00nul@x> <good@x>\n\nHi.\n")
	got := MessageIDs(h)
	want := []string{"<good@x>"}
	if !equalStrings(got, want) {
		t.Fatalf("MessageIDs = %q, want %q — a non-ASCII token must never reach a lookup", got, want)
	}
}

// The length cap, which the ASCII check alone would not catch: RFC 5322 bounds a
// header line at 998 octets, so anything longer is padding rather than an id.
func TestMessageIDsDropsAnOverlongToken(t *testing.T) {
	long := "<" + strings.Repeat("a", maxMessageIDLen) + ">"
	h := hdrOnly(t, "From: bob@example.com\nTo: alice@example.com\nSubject: Re: Hello\n"+
		"References: <good@x> "+long+"\n\nHi.\n")
	got := MessageIDs(h)
	want := []string{"<good@x>"}
	if !equalStrings(got, want) {
		t.Fatalf("MessageIDs = %d tokens, want only the short one", len(got))
	}
}

// The guard itself, at the edges, so the accept/reject boundary is pinned rather
// than inferred from the two callers above.
func TestUsableMessageID(t *testing.T) {
	cases := map[string]bool{
		"<a@b>":                              true,
		"<a b@c>":                            true,  // space is printable; an interior space is odd, not dangerous
		"<~!#$%&'*+/=?^_`{|}-@x>":            true,  // every atext character RFC 5322 permits
		"":                                   false, // an absent id identifies nothing
		"<\xff@x>":                           false,
		"<\x00@x>":                           false,
		"<a\nb@x>":                           false, // a bare control character
		"<" + strings.Repeat("a", 997) + ">": false, // 999 bytes, one over the RFC line bound
		"<" + strings.Repeat("a", 996) + ">": true,  // 998 bytes exactly
		"<héllo@x>":                          false, // valid UTF-8, still not US-ASCII
	}
	for in, want := range cases {
		if got := usableMessageID(in); got != want {
			t.Errorf("usableMessageID(%q) = %v, want %v", in, got, want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
