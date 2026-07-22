package inbox

import (
	"io"
	"net/mail"
	"strings"
	"testing"
)

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
