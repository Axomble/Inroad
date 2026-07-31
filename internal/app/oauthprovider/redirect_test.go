package oauthprovider

import (
	"net/url"
	"testing"
)

func TestValidateRedirectURI(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		ok   bool
	}{
		{"https absolute", "https://app.example.com/cb", true},
		{"https with port + query", "https://app.example.com:8443/cb?x=1", true},
		{"loopback ipv4", "http://127.0.0.1/callback", true},
		{"loopback ipv4 with port", "http://127.0.0.1:52123/callback", true},
		{"loopback localhost", "http://localhost:8080/cb", true},
		{"loopback ipv6", "http://[::1]:9000/cb", true},

		{"empty", "", false},
		{"plain http non-loopback", "http://app.example.com/cb", false},
		{"javascript scheme", "javascript:alert(1)", false},
		{"data scheme", "data:text/html,hi", false},
		{"ftp scheme", "ftp://host/cb", false},
		{"custom app scheme", "com.example.app:/cb", false},
		{"relative", "/callback", false},
		{"https no host", "https:///cb", false},
		{"https with fragment", "https://app.example.com/cb#frag", false},
		{"https with bare hash", "https://app.example.com/cb#", false},
		{"127.x non-loopback literal", "http://127.0.0.2/cb", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateRedirectURI(c.uri)
			if c.ok && err != nil {
				t.Fatalf("want accept, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatalf("want reject, got accept")
			}
		})
	}
}

func TestContainsExactRejectsLookalikes(t *testing.T) {
	registered := []string{"https://app.example.com/cb"}
	bad := []string{
		"https://app.example.com/cb/",    // trailing slash
		"https://app.example.com/cb?x=1", // extra query
		"https://app.example.com/cb2",    // suffix
		"https://app.example.com",        // prefix
		"https://evil.example.com/cb",    // different host
		"http://app.example.com/cb",      // different scheme
		"https://app.example.com:443/cb", // added explicit port
		" https://app.example.com/cb",    // leading space
		"https://APP.example.com/cb",     // case
	}
	if !containsExact(registered, "https://app.example.com/cb") {
		t.Fatal("exact match must be accepted")
	}
	for _, b := range bad {
		if containsExact(registered, b) {
			t.Fatalf("look-alike must NOT match: %q", b)
		}
	}
}

func TestBuildRedirectEchoesStateAndMergesQuery(t *testing.T) {
	got, err := buildRedirect("https://app.example.com/cb?keep=1", map[string]string{
		"code":  "abc",
		"state": "xyz",
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("keep") != "1" {
		t.Errorf("pre-existing query dropped: %s", got)
	}
	if q.Get("code") != "abc" || q.Get("state") != "xyz" {
		t.Errorf("code/state not set: %s", got)
	}
	if u.Fragment != "" {
		t.Errorf("must not add a fragment: %s", got)
	}
}

func TestBuildRedirectOmitsEmptyState(t *testing.T) {
	got, err := buildRedirect("https://app.example.com/cb", map[string]string{
		"code":  "abc",
		"state": "", // absent state must not be echoed
	})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	if _, has := u.Query()["state"]; has {
		t.Errorf("empty state must be omitted: %s", got)
	}
}
