package inprocess

import "testing"

// The angle-bracket normalisation is the one piece of the header-loss lookup that
// is pure, and it is the piece a raw string compare gets wrong: RFC 5322 makes <>
// part of the Message-ID FIELD rather than of the identifier, and providers are
// inconsistent about echoing them. The matching itself (both stored forms, the
// workspace + recipient pin) is exercised by the //go:build integration tests.
func TestNormalizeMessageIDIgnoresAngleBracketsAndSurroundingSpace(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bracketed, as most providers echo it", "<abc@mail.example>", "abc@mail.example"},
		{"bare, as some providers echo it", "abc@mail.example", "abc@mail.example"},
		{"folded header leaves surrounding space", "  <abc@mail.example>\t", "abc@mail.example"},
		{"space inside the brackets", "< abc@mail.example >", "abc@mail.example"},
		{"empty stays empty so the caller can refuse it", "", ""},
		{"brackets only", "<>", ""},
		// A '<' or '>' INSIDE the identifier is not a delimiter, so only the
		// outermost pair may be removed — Trim would eat a run of them.
		{"only the outer pair is removed", "<<abc@mail.example>>", "<abc@mail.example>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeMessageID(tc.in); got != tc.want {
				t.Errorf("normalizeMessageID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
