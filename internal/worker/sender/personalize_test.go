package sender

import "testing"

func TestPersonalize(t *testing.T) {
	out := personalize("Hi {{first_name}} ({{email}})", "Alice", "alice@x.com")
	if out != "Hi Alice (alice@x.com)" {
		t.Fatalf("got %q", out)
	}
	if got := personalize("Hi {{first_name}}", "", "a@b.com"); got != "Hi there" {
		t.Fatalf("empty name: got %q", got)
	}
}
