package auth

import "testing"

// OAuthGrantableScopes is a curated, security-sensitive allowlist. These tests guard
// it against silent drift: every grantable scope must be a real, known scope, and the
// three dangerous capabilities must stay excluded. A regression here would either
// authorize nothing (a grantable scope absent from AllScopes) or, worse, widen what a
// third-party OAuth client can reach.
func TestOAuthGrantableScopesAreKnown(t *testing.T) {
	for _, s := range OAuthGrantableScopes {
		if !IsKnownScope(s) {
			t.Errorf("OAuthGrantableScopes contains %q which is not in AllScopes (authorizes no route)", s)
		}
	}
}

func TestOAuthGrantableScopesExcludeDangerous(t *testing.T) {
	// Sending, campaign mutation, and mailbox mutation must never be delegable to a
	// third-party client. The frontend picker (web oauth-scopes.ts) mirrors this set.
	for _, banned := range []string{ScopeCampaignsSend, ScopeCampaignsWrite, ScopeMailboxesWrite} {
		if IsOAuthGrantableScope(banned) {
			t.Errorf("%q must NOT be OAuth-grantable", banned)
		}
	}
}

func TestOAuthGrantableScopesExactSet(t *testing.T) {
	// Pin the exact set so any change is a conscious, reviewed edit (and stays in sync
	// with the frontend's OAUTH_GRANTABLE_SCOPES assertion).
	want := map[string]bool{
		ScopeMailboxesRead: true,
		ScopeCampaignsRead: true,
		ScopeContactsRead:  true,
		ScopeContactsWrite: true,
		ScopeListsRead:     true,
		ScopeListsWrite:    true,
	}
	if len(OAuthGrantableScopes) != len(want) {
		t.Fatalf("OAuthGrantableScopes has %d entries, want %d", len(OAuthGrantableScopes), len(want))
	}
	for _, s := range OAuthGrantableScopes {
		if !want[s] {
			t.Errorf("unexpected grantable scope %q", s)
		}
	}
}
