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
	ScopeCRMRead        = "crm:read"
	ScopeCRMWrite       = "crm:write"
	ScopeListsRead      = "lists:read"
	ScopeListsWrite     = "lists:write"
	ScopeCampaignsSend  = "campaigns:send"
	// ScopeDeliverabilityWrite authorizes POST /deliverability/events — an
	// external pipeline (an SES SNS subscriber, a provider webhook) reporting a
	// complaint or bounce. It is its own scope rather than campaigns:write
	// because that is the whole of what such a pipeline needs: an ingest
	// credential must not also carry the authority to mutate campaigns.
	ScopeDeliverabilityWrite = "deliverability:write"
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
	ScopeCRMRead,
	ScopeCRMWrite,
	ScopeListsRead,
	ScopeListsWrite,
	ScopeDeliverabilityWrite,
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

// OAuthGrantableScopes is the STRICT subset of the vocabulary a third-party OAuth
// client (the oauthprovider authorization server) may EVER be granted. It is the
// single source of truth for that cap: dynamic client registration rejects a
// requested scope outside this set, and /authorize additionally requires the
// requested scope be a subset of both the client's registered scopes AND this set.
//
// The set structurally EXCLUDES every dangerous capability so a delegated
// third-party grant can never reach one:
//
//   - ScopeCampaignsSend — sending mail is the single highest-abuse capability
//     (spam, sender-reputation damage, real cost); it must never be reachable via a
//     delegated grant, only by a logged-in human or a workspace-minted API key.
//   - ScopeCampaignsWrite — mutating a sequence/campaign is a step toward triggering
//     sends and can destructively alter a live campaign.
//   - ScopeMailboxesWrite — mutates mailbox connections/credentials, which is
//     security-sensitive infrastructure, not third-party data access.
//   - ScopeDeliverabilityWrite — an ingested event suppresses an address
//     workspace-wide and can trip a campaign's circuit breaker. A third party that
//     could forge complaints could suppress a workspace's contacts and stop its
//     campaigns; that belongs to a workspace-minted key, not a delegated grant.
//
// Admin and API-key management are NOT scopes at all (they are session + admin-role
// gated, never scope-gated — see RequireRole), so they are structurally unreachable
// through any grant regardless of this list. What remains is read access plus the
// two low-risk data-entry writes (contacts/lists) a legitimate integration needs.
var OAuthGrantableScopes = []string{
	ScopeMailboxesRead,
	ScopeCampaignsRead,
	ScopeContactsRead,
	ScopeContactsWrite,
	ScopeCRMRead,
	ScopeCRMWrite,
	ScopeListsRead,
	ScopeListsWrite,
}

// IsOAuthGrantableScope reports whether scope may be granted to a third-party OAuth
// client. A scope absent from OAuthGrantableScopes (unknown, or a deliberately
// excluded privileged/destructive one) is not grantable.
func IsOAuthGrantableScope(scope string) bool {
	for _, s := range OAuthGrantableScopes {
		if s == scope {
			return true
		}
	}
	return false
}
