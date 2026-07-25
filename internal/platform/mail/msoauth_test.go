package mail

import (
	"strings"
	"testing"
)

func TestMicrosoftOAuthEnabled(t *testing.T) {
	if (MicrosoftOAuth{}).Enabled() {
		t.Fatal("empty config must be disabled")
	}
	if !(MicrosoftOAuth{ClientID: "a", ClientSecret: "b"}).Enabled() {
		t.Fatal("configured must be enabled")
	}
}

func TestMicrosoftOAuthConfigScopes(t *testing.T) {
	cfg := MicrosoftOAuth{ClientID: "a", ClientSecret: "b"}.Config()
	want := []string{
		"https://graph.microsoft.com/Mail.Send",
		"https://graph.microsoft.com/Mail.Read",
		"https://graph.microsoft.com/User.Read",
		"offline_access",
		"openid",
		"email",
	}
	if len(cfg.Scopes) != len(want) {
		t.Fatalf("scope count = %d, want %d: %v", len(cfg.Scopes), len(want), cfg.Scopes)
	}
	for i, s := range want {
		if cfg.Scopes[i] != s {
			t.Fatalf("scope[%d] = %q, want %q", i, cfg.Scopes[i], s)
		}
	}
	if cfg.Endpoint.AuthURL == "" {
		t.Fatal("endpoint AuthURL must be non-empty")
	}
}

func TestMicrosoftOAuthConfigDefaultTenantCommon(t *testing.T) {
	cfg := MicrosoftOAuth{ClientID: "a", ClientSecret: "b"}.Config()
	if !strings.Contains(cfg.Endpoint.AuthURL, "/common/") {
		t.Fatalf("blank tenant must yield the common authority, got AuthURL %q", cfg.Endpoint.AuthURL)
	}
}
