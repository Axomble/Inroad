package oauthprovider

import (
	"strings"
	"testing"
)

func TestIsValidCodeChallenge(t *testing.T) {
	valid43 := strings.Repeat("a", 43) // min length, unreserved
	valid128 := strings.Repeat("A1-._~", 22)[:128]
	cases := []struct {
		name string
		c    string
		ok   bool
	}{
		{"exact 43 unreserved", valid43, true},
		{"128 unreserved", valid128, true},
		{"too short", strings.Repeat("a", 42), false},
		{"too long", strings.Repeat("a", 129), false},
		{"illegal plus", strings.Repeat("a", 42) + "+", false},
		{"illegal slash", strings.Repeat("a", 42) + "/", false},
		{"illegal space", strings.Repeat("a", 42) + " ", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isValidCodeChallenge(c.c); got != c.ok {
				t.Fatalf("isValidCodeChallenge(%q)=%v want %v", c.c, got, c.ok)
			}
		})
	}
}

func TestParseScopeDedupesAndTrims(t *testing.T) {
	got := parseScope("  contacts:read   contacts:read  lists:read ")
	if len(got) != 2 || got[0] != "contacts:read" || got[1] != "lists:read" {
		t.Fatalf("parseScope = %v", got)
	}
	if len(parseScope("")) != 0 {
		t.Fatal("empty scope must parse to empty slice")
	}
}

func TestIsSubset(t *testing.T) {
	have := []string{"a", "b", "c"}
	if !isSubset([]string{"a", "c"}, have) {
		t.Fatal("want subset")
	}
	if isSubset([]string{"a", "z"}, have) {
		t.Fatal("z not in have")
	}
	if !isSubset(nil, have) {
		t.Fatal("empty is always a subset")
	}
}
