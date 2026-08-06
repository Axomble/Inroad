package mime_test

import (
	"strings"
	"testing"

	"github.com/inroad/inroad/internal/platform/mime"
)

func TestExtractPlainTextOnly(t *testing.T) {
	plain, html, err := mime.Extract("text/plain; charset=utf-8", []byte("Hi there, thanks!"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plain != "Hi there, thanks!" {
		t.Fatalf("plain = %q", plain)
	}
	if html != "" {
		t.Fatalf("html = %q, want empty", html)
	}
}

func TestExtractMultipartAlternativeQuotedPrintable(t *testing.T) {
	raw := "--b1\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"Caf=C3=A9 sounds great\r\n" +
		"--b1\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"<p>Caf=C3=A9 sounds great</p>\r\n" +
		"--b1--\r\n"
	plain, html, err := mime.Extract(`multipart/alternative; boundary="b1"`, []byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plain != "Café sounds great" {
		t.Fatalf("plain = %q", plain)
	}
	if html != "<p>Café sounds great</p>" {
		t.Fatalf("html = %q", html)
	}
}

func TestExtractMultipartBase64(t *testing.T) {
	// base64 of "Thanks a lot!"
	const b64 = "VGhhbmtzIGEgbG90IQ=="
	raw := "--b2\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		b64 + "\r\n" +
		"--b2--\r\n"
	plain, _, err := mime.Extract(`multipart/mixed; boundary="b2"`, []byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plain != "Thanks a lot!" {
		t.Fatalf("plain = %q", plain)
	}
}

func TestExtractIgnoresNonTextParts(t *testing.T) {
	raw := "--b3\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"See attached\r\n" +
		"--b3\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		"JVBERi0xLjQK\r\n" +
		"--b3--\r\n"
	plain, html, err := mime.Extract(`multipart/mixed; boundary="b3"`, []byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plain != "See attached" {
		t.Fatalf("plain = %q", plain)
	}
	if html != "" {
		t.Fatalf("html = %q, want empty (pdf part must be skipped)", html)
	}
}

func TestExtractMalformedMultipartDoesNotError(t *testing.T) {
	// No boundary in the header at all — multipart.NewReader can't be built.
	plain, html, err := mime.Extract("multipart/alternative", []byte("garbage, no boundary"))
	if err != nil {
		t.Fatalf("malformed input must not error, got: %v", err)
	}
	if plain != "garbage, no boundary" {
		t.Fatalf("plain = %q, want the raw body as a best-effort fallback", plain)
	}
	if html != "" {
		t.Fatalf("html = %q, want empty", html)
	}
}

func TestExtractNestedMultipartMixedOfAlternative(t *testing.T) {
	raw := "--outer\r\n" +
		"Content-Type: multipart/alternative; boundary=\"inner\"\r\n\r\n" +
		"--inner\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"Plain body\r\n" +
		"--inner\r\n" +
		"Content-Type: text/html\r\n\r\n" +
		"<p>HTML body</p>\r\n" +
		"--inner--\r\n" +
		"--outer\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		"AAAA\r\n" +
		"--outer--\r\n"
	plain, html, err := mime.Extract(`multipart/mixed; boundary="outer"`, []byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(plain, "Plain body") {
		t.Fatalf("plain = %q", plain)
	}
	if !strings.Contains(html, "HTML body") {
		t.Fatalf("html = %q", html)
	}
}
