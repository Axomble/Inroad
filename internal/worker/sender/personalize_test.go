package sender

import (
	"strings"
	"testing"
)

func TestPersonalizeText(t *testing.T) {
	out := personalizeText("Hi {{first_name}} ({{email}})", "Alice", "alice@x.com")
	if out != "Hi Alice (alice@x.com)" {
		t.Fatalf("got %q", out)
	}
	if got := personalizeText("Hi {{first_name}}", "", "a@b.com"); got != "Hi there" {
		t.Fatalf("empty name: got %q", got)
	}
}

// TestPersonalizeHTMLEscapes verifies that substituted values are HTML-escaped
// in the HTML variant. Without this a hostile first-name like <script> would
// be injected verbatim into the rendered email.
func TestPersonalizeHTMLEscapes(t *testing.T) {
	out := personalizeHTML("<p>Hi {{first_name}}</p>", "<script>alert(1)</script>", "a@b.com")
	if strings.Contains(out, "<script>") {
		t.Fatalf("expected escaped output, got %q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("expected escaped <script>, got %q", out)
	}
}

// TestPersonalizeTextDoesNotEscape guards the invariant that plaintext bodies
// pass values through verbatim - HTML escaping in a text/plain body would
// leak "&lt;" markers into the message the recipient reads.
func TestPersonalizeTextDoesNotEscape(t *testing.T) {
	out := personalizeText("Hi {{first_name}}", "<Alice>", "a@b.com")
	if !strings.Contains(out, "<Alice>") {
		t.Fatalf("expected verbatim <Alice> in text body, got %q", out)
	}
}
