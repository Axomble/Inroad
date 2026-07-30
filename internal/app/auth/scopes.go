package auth

// Scopes are the coarse-grained capabilities a non-session principal (API key
// or OAuth grant, added in later phases) may be granted. This file is the
// single source of truth for the scope vocabulary: middleware, DTOs, and any
// future grant UI must reference these constants rather than raw strings.
//
// A KindSession principal (a logged-in human) implicitly holds EVERY scope —
// scopes exist only to attenuate machine principals below their owner's
// authority (see Principal.HasScope). The set is intentionally coarse for now
// (read/write per domain); it can be refined without breaking the seam.
const (
	ScopeMailboxesRead  = "mailboxes:read"
	ScopeMailboxesWrite = "mailboxes:write"
	ScopeCampaignsRead  = "campaigns:read"
	ScopeCampaignsWrite = "campaigns:write"
	ScopeContactsRead   = "contacts:read"
	ScopeContactsWrite  = "contacts:write"
	ScopeListsRead      = "lists:read"
	ScopeListsWrite     = "lists:write"
	ScopeCampaignsSend  = "campaigns:send"
)

// AllScopes is every scope the server currently understands. A grant carrying
// a scope absent from this set is meaningless (it authorizes no route), so
// grant-issuing paths in later phases should validate against it.
var AllScopes = []string{
	ScopeMailboxesRead,
	ScopeMailboxesWrite,
	ScopeCampaignsRead,
	ScopeCampaignsWrite,
	ScopeCampaignsSend,
	ScopeContactsRead,
	ScopeContactsWrite,
	ScopeListsRead,
	ScopeListsWrite,
}

// IsKnownScope reports whether scope is part of the server's vocabulary.
func IsKnownScope(scope string) bool {
	for _, s := range AllScopes {
		if s == scope {
			return true
		}
	}
	return false
}
