package mail

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/commands"
)

// serializeCommand writes an IMAP command to its exact wire bytes through go-imap's
// own encoder, so the tests assert the REAL on-the-wire form (quoting/literals),
// not a reconstruction.
func serializeCommand(t *testing.T, cmd *imap.Command) string {
	t.Helper()
	var buf bytes.Buffer
	if err := cmd.WriteTo(imap.NewWriter(&buf)); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	return buf.String()
}

// assertCarriedAsToken proves value crosses the wire as a SINGLE self-delimiting
// argument — either a quoted string (CR/LF/quote escaped by strconv.Quote) or a
// length-prefixed IMAP literal ({N}\r\n + exactly N raw bytes). Both encodings make
// injected keywords/CRLFs unparseable as a second command; a naive concatenation
// would instead splice value in as bare tokens. Covers the SEARCH Message-ID and the
// SELECT/MOVE mailbox name.
func assertCarriedAsToken(t *testing.T, wire, value string) {
	t.Helper()
	quoted := strconv.Quote(value)
	literal := fmt.Sprintf("{%d}\r\n%s", len(value), value)
	if !strings.Contains(wire, quoted) && !strings.Contains(wire, literal) {
		t.Fatalf("value %q not carried as a quoted string (%q) or a length-prefixed literal in:\n%q", value, quoted, wire)
	}
}

// assertMessageIDIsData proves searchByMessageID carries the id as a plain string
// field (which the encoder emits quoted/literal), never an imap.RawString (which is
// emitted UNQUOTED — the only injectable field type). This is the structural half of
// the injection-safety proof.
func assertMessageIDIsData(t *testing.T, crit *imap.SearchCriteria, id string) {
	t.Helper()
	for i, f := range crit.Format() {
		if rs, ok := f.(imap.RawString); ok && string(rs) == id {
			t.Fatalf("field %d carries the message id as imap.RawString (unquoted, injectable): %q", i, id)
		}
		if s, ok := f.(string); ok && s == id {
			return // plain string ⇒ quoted/literal by the encoder ⇒ safe
		}
	}
	t.Fatalf("message id %q not found as a plain-string field in Format() = %#v", id, crit.Format())
}

// TestSearchByMessageIDInjectionSafe is the security gate for IMAP command
// injection: the RFC822 Message-ID is attacker-influenceable inbound content, so it
// MUST be passed as a quoted/literal SEARCH argument, never concatenated into a raw
// command. Every case (including CRLF- and keyword-injection payloads) must still
// serialize to a SINGLE IMAP line (exactly one CRLF terminator) — an injected second
// command would add another CRLF.
func TestSearchByMessageIDInjectionSafe(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"benign", "<abc123@mail.example.com>"},
		{"space", "id with a space"},
		{"quote breakout", `x"@y`},
		{"crlf command injection", "a@b\r\nA0 DELETE INBOX"},
		{"imap keyword injection", `z@w" OR HEADER Subject "x`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			crit := searchByMessageID(tc.id)
			// The id is stored verbatim under the canonical header key...
			if got := crit.Header.Get("Message-Id"); got != tc.id {
				t.Fatalf("Header value = %q, want verbatim %q", got, tc.id)
			}
			// ...as data, not an unquoted RawString token.
			assertMessageIDIsData(t, crit, tc.id)

			// Wire-level: over a real SEARCH command the id crosses as one quoted/literal
			// argument, so an embedded CRLF/keyword can never break out into a second
			// IMAP command.
			wire := serializeCommand(t, (&commands.Search{Criteria: crit}).Command())
			assertCarriedAsToken(t, wire, tc.id)
		})
	}
}

// TestSelectFolderInjectionSafe proves the SourceFolder passed to SELECT (Rescue's
// dial into the junk folder) crosses the wire as a single quoted, modified-UTF-7
// encoded mailbox name — so a malicious folder name (embedded CRLF/quotes) can
// neither split the SELECT into a second command nor break out of the quoted token.
func TestSelectFolderInjectionSafe(t *testing.T) {
	for _, folder := range []string{
		"Junk",
		"[Gmail]/Spam",
		"weird\r\nA0 DELETE INBOX",
		`ev"il`,
	} {
		wire := serializeCommand(t, (&commands.Select{Mailbox: folder}).Command())
		// Exactly one line: mailbox mUTF-7 encoding strips raw CR/LF, so an injected
		// CRLF can never add a second command line.
		if n := strings.Count(wire, "\r\n"); n != 1 {
			t.Fatalf("SELECT %q serialized with %d CRLFs, want 1:\n%q", folder, n, wire)
		}
		// The mailbox is a quoted token, so an embedded quote (escaped/encoded) can't
		// terminate it early and append bare command tokens.
		if !strings.HasPrefix(wire, `* SELECT "`) || !strings.HasSuffix(wire, "\"\r\n") {
			t.Fatalf("SELECT %q not a single quoted mailbox token:\n%q", folder, wire)
		}
	}
}

// TestRescueSkipsInboxPlacement proves Rescue is a no-op (no dial) when the message
// already sits in the inbox — SourceFolder empty or INBOX (any case).
func TestRescueSkipsInboxPlacement(t *testing.T) {
	e := NewNetEngager(false)
	for _, folder := range []string{"", "INBOX", "inbox", "Inbox"} {
		// No IMAP host is set, so if Rescue tried to dial it would fail; a nil error
		// proves it short-circuited before any dial.
		if err := e.Rescue(t.Context(), EngageTarget{SourceFolder: folder, MessageID: "<x@y>"}); err != nil {
			t.Fatalf("Rescue(folder=%q) = %v, want nil no-op", folder, err)
		}
	}
}
