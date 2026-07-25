package inbox

import "testing"

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
