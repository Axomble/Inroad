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
	ScopeInboxRead      = "inbox:read"
	ScopeInboxWrite     = "inbox:write"
	// ScopeInboxSend authorizes POST /inbox/threads/{id}/reply — sending a
	// manual reply from the unified inbox. Deliberately its own scope rather
	// than folded into ScopeInboxWrite: that scope authorizes only the
	// boolean unread/read toggle, which mutates no business data and exposes
	// no reply content, whereas sending mail is the campaigns:send-class
	// highest-abuse capability (spam, sender-reputation damage, real cost).
	// It is in AllScopes (a workspace-minted API key or a logged-in human may
	// send a reply) but deliberately absent from OAuthGrantableScopes — see
	// that list's own doc comment for why sending is never delegable to a
	// third-party client.
	ScopeInboxSend = "inbox:send"
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
	ScopeInboxRead,
	ScopeInboxWrite,
	ScopeInboxSend,
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
//   - ScopeCampaignsSend / ScopeInboxSend — sending mail is the single
//     highest-abuse capability (spam, sender-reputation damage, real cost); it
//     must never be reachable via a delegated grant, only by a logged-in human
//     or a workspace-minted API key. ScopeInboxSend covers the same risk for a
//     manual reply sent from the unified inbox.
//   - ScopeCampaignsWrite — mutating a sequence/campaign is a step toward triggering
//     sends and can destructively alter a live campaign.
//   - ScopeMailboxesWrite — mutates mailbox connections/credentials, which is
//     security-sensitive infrastructure, not third-party data access.
//   - ScopeDeliverabilityWrite — an ingested event suppresses an address
//     workspace-wide and can trip a campaign's circuit breaker. A third party that
//     could forge complaints could suppress a workspace's contacts and stop its
//     campaigns; that belongs to a workspace-minted key, not a delegated grant.
//   - ScopeInboxRead — reply bodies are free-text correspondence content, a
//     materially more sensitive category than the structured CRM/contact data this
//     set already grants; reading a workspace's inbound mail is excluded even
//     though reading its contacts is not.
//
// ScopeInboxWrite, by contrast, IS granted below: it authorizes only the boolean
// unread/read toggle on a thread (PUT /inbox/threads/{id}/read). It exposes no
// reply content and mutates no business data (no campaign, no contact, no send),
// so by this file's own criteria it is no more dangerous than the other included
// writes (contacts/lists) — nothing above justifies excluding it.
//
// Admin and API-key management are NOT scopes at all (they are session + admin-role
// gated, never scope-gated — see RequireRole), so they are structurally unreachable
// through any grant regardless of this list. What remains is read access plus the
// low-risk writes (contacts, lists, and marking an inbox thread read) a legitimate
// integration needs.
var OAuthGrantableScopes = []string{
	ScopeMailboxesRead,
	ScopeCampaignsRead,
	ScopeContactsRead,
	ScopeContactsWrite,
	ScopeCRMRead,
	ScopeCRMWrite,
	ScopeListsRead,
	ScopeListsWrite,
	ScopeInboxWrite,
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
