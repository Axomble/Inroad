package track

import "testing"

var testSecret = []byte("test-secret-key")

func TestOpenToken_RoundTrip(t *testing.T) {
	sendID := "8f14e45f-ceea-467e-adc1-0000000000ab"

	token := MakeOpenToken(testSecret, sendID)
	gotSendID, ok := ParseOpenToken(testSecret, token)

	if !ok {
		t.Fatalf("ParseOpenToken() ok = false, want true")
	}
	if gotSendID != sendID {
		t.Errorf("gotSendID = %q, want %q", gotSendID, sendID)
	}
}

func TestClickToken_RoundTrip(t *testing.T) {
	sendID := "8f14e45f-ceea-467e-adc1-0000000000ab"
	url := "https://example.com/product?utm_source=inroad&x=1"

	token := MakeClickToken(testSecret, sendID, url)
	gotSendID, gotURL, ok := ParseClickToken(testSecret, token)

	if !ok {
		t.Fatalf("ParseClickToken() ok = false, want true")
	}
	if gotSendID != sendID {
		t.Errorf("gotSendID = %q, want %q", gotSendID, sendID)
	}
	if gotURL != url {
		t.Errorf("gotURL = %q, want %q", gotURL, url)
	}
}

func TestClickToken_RoundTrip_URLContainingDelimiterByte(t *testing.T) {
	sendID := "8f14e45f-ceea-467e-adc1-0000000000ab"
	// A URL that happens to contain a literal NUL byte (the internal
	// separator) must still round-trip via the first-occurrence split,
	// since sendID is a fixed-format UUID that never contains NUL.
	url := "https://example.com/path?q=\x00trailing"

	token := MakeClickToken(testSecret, sendID, url)
	gotSendID, gotURL, ok := ParseClickToken(testSecret, token)

	if !ok {
		t.Fatalf("ParseClickToken() ok = false, want true")
	}
	if gotSendID != sendID {
		t.Errorf("gotSendID = %q, want %q", gotSendID, sendID)
	}
	if gotURL != url {
		t.Errorf("gotURL = %q, want %q", gotURL, url)
	}
}

func TestOpenToken_TamperedSignature(t *testing.T) {
	sendID := "8f14e45f-ceea-467e-adc1-0000000000ab"
	token := MakeOpenToken(testSecret, sendID)

	tampered := token[:len(token)-1] + "x"
	if tampered == token {
		tampered = token[:len(token)-1] + "y"
	}

	_, ok := ParseOpenToken(testSecret, tampered)
	if ok {
		t.Fatalf("ParseOpenToken() ok = true for tampered token, want false")
	}
}

func TestClickToken_TamperedSignature(t *testing.T) {
	sendID := "8f14e45f-ceea-467e-adc1-0000000000ab"
	url := "https://example.com/product"
	token := MakeClickToken(testSecret, sendID, url)

	tampered := token[:len(token)-1] + "x"
	if tampered == token {
		tampered = token[:len(token)-1] + "y"
	}

	_, _, ok := ParseClickToken(testSecret, tampered)
	if ok {
		t.Fatalf("ParseClickToken() ok = true for tampered token, want false")
	}
}

// TestClickToken_AlteredURL is the open-redirect regression test: the URL
// lives inside the signed payload, so changing it (without re-signing,
// which requires the secret) must invalidate the token. We keep the
// original signature half and swap in a payload for a different URL —
// simulating an attacker who can only control the redirect target.
func TestClickToken_AlteredURL(t *testing.T) {
	sendID := "8f14e45f-ceea-467e-adc1-0000000000ab"
	legit := MakeClickToken(testSecret, sendID, "https://example.com/legit")
	evil := MakeClickToken(testSecret, sendID, "https://evil.example/phish")

	dot := indexByte(legit, '.')
	evilDot := indexByte(evil, '.')
	tampered := evil[:evilDot] + legit[dot:] // attacker's payload + legit's signature

	_, _, ok := ParseClickToken(testSecret, tampered)
	if ok {
		t.Fatalf("ParseClickToken() ok = true for tampered (altered URL) token, want false")
	}
}

func TestParseOpenToken_RejectsClickToken(t *testing.T) {
	sendID := "8f14e45f-ceea-467e-adc1-0000000000ab"
	clickToken := MakeClickToken(testSecret, sendID, "https://example.com")

	_, ok := ParseOpenToken(testSecret, clickToken)
	if ok {
		t.Fatalf("ParseOpenToken() ok = true for a click token, want false")
	}
}

func TestParseClickToken_RejectsOpenToken(t *testing.T) {
	sendID := "8f14e45f-ceea-467e-adc1-0000000000ab"
	openToken := MakeOpenToken(testSecret, sendID)

	_, _, ok := ParseClickToken(testSecret, openToken)
	if ok {
		t.Fatalf("ParseClickToken() ok = true for an open token, want false")
	}
}

func TestParseOpenToken_GarbageInput(t *testing.T) {
	cases := []string{"", "not-a-token", "no-dot-here", ".", "..", "a.b.c"}
	for _, in := range cases {
		if _, ok := ParseOpenToken(testSecret, in); ok {
			t.Errorf("ParseOpenToken(%q) ok = true, want false", in)
		}
	}
}

func TestParseClickToken_GarbageInput(t *testing.T) {
	cases := []string{"", "not-a-token", "no-dot-here", ".", "..", "a.b.c"}
	for _, in := range cases {
		if _, _, ok := ParseClickToken(testSecret, in); ok {
			t.Errorf("ParseClickToken(%q) ok = true, want false", in)
		}
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
