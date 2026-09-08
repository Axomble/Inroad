package inbox

import (
	"errors"
	"io"
	"net/mail"
	"strings"
	"testing"
	"unicode/utf8"
)

// errInvalidEncoding models the ONE store refusal a convenience fake would
// silently absorb: Postgres rejects a text parameter carrying a byte it cannot
// encode with SQLSTATE 22021 ("invalid byte sequence for encoding \"UTF8\""),
// reproduced against the dev database for both a raw 0xFF and a NUL. It is
// neither pgx.ErrNoRows/coreapi.ErrNoMatch nor transient, so it propagates out
// of the poll — which is what wedges the mailbox's cursor.
//
// Every fake in this package that takes a Message-ID as a lookup key models it,
// because a fake that answered "no match" for a key the real database cannot
// even be asked about would make that wedge untestable.
var errInvalidEncoding = errors.New(`ERROR: invalid byte sequence for encoding "UTF8" (SQLSTATE 22021)`)

// encodableByPostgres reports whether a text parameter carrying v would be
// accepted at all. NUL is checked separately from UTF-8 validity because it is
// VALID UTF-8 and Postgres still refuses it.
func encodableByPostgres(v string) bool {
	return utf8.ValidString(v) && !strings.ContainsRune(v, 0)
}

// crlf converts a fixture written with plain \n line endings (readable in
// source) to the \r\n endings real SMTP/IMAP transport uses, so parsing
// behaves the same as it would against a live mailbox.
func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

// parseFixture parses a raw RFC 5322 message and returns the header, its
// Content-Type, and the raw body bytes — exactly the shape ParseDSN expects.
func parseFixture(t *testing.T, raw string) (mail.Header, string, []byte) {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(crlf(raw)))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		t.Fatalf("read fixture body: %v", err)
	}
	return msg.Header, msg.Header.Get("Content-Type"), body
}

// hdrOnly is a lighter helper for the reply-matcher tests, which only need
// the parsed header.
func hdrOnly(t *testing.T, raw string) mail.Header {
	t.Helper()
	h, _, _ := parseFixture(t, raw)
	return h
}
