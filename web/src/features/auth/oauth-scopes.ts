// Human-readable descriptions for the OAuth-grantable scopes, shown on the consent
// screen (full sentences, second person — "Read your contacts") and in the
// connected-apps register dialog (the domain-grouped multi-select).
//
// The grantable set mirrors the backend's OAuth rule (internal/app/auth/scopes.go):
// read/write on the data domains, but NOT send / api-key / admin scopes. We derive
// it from the API-key scope groups (the shared source of truth for the picker) by
// dropping every non-grantable scope, so there is one scope vocabulary to maintain.
import { API_KEY_SCOPE_GROUPS, type ScopeGroup } from './api-key-scopes'

// Scopes an API key may hold that an OAuth client may NOT be granted. This MUST
// mirror the backend's exclusion (internal/app/auth/scopes.go OAuthGrantableScopes,
// enforced at registration + /authorize): sending, campaign mutation, and mailbox
// mutation are never delegable to a third-party client. Offering one here would only
// produce a confusing hard failure at submit, since the backend fails closed.
const OAUTH_EXCLUDED_SCOPES = new Set<string>([
  'campaigns:send',
  'campaigns:write',
  'mailboxes:write',
  // Reply content is materially more sensitive than structured CRM/contact
  // data, so reading the inbox is not delegable; the read/unread toggle
  // (inbox:write) is. Mirrors the backend's rationale in scopes.go.
  'inbox:read',
  // An ingested complaint suppresses an address workspace-wide and can trip a
  // campaign's circuit breaker — never delegable to a third-party client.
  'deliverability:write',
])

/** The domain-grouped, OAuth-grantable subset of the API-key scope picker. */
export const OAUTH_SCOPE_GROUPS: readonly ScopeGroup[] = API_KEY_SCOPE_GROUPS.map((group) => ({
  domain: group.domain,
  scopes: group.scopes.filter((scope) => !OAUTH_EXCLUDED_SCOPES.has(scope.value)),
})).filter((group) => group.scopes.length > 0)

/** Every OAuth-grantable scope value, flattened. */
export const OAUTH_GRANTABLE_SCOPES: readonly string[] = OAUTH_SCOPE_GROUPS.flatMap((group) =>
  group.scopes.map((scope) => scope.value),
)

/**
 * Consent-screen sentence for each grantable scope. Second person and plain so the
 * resource owner understands exactly what they are granting. Kept exhaustive over
 * `OAUTH_GRANTABLE_SCOPES` (asserted by a test); an unmapped scope falls back to its
 * raw value rather than being silently hidden — a user must never be shown less than
 * what an app can actually do.
 */
const SCOPE_CONSENT_LABELS: Record<string, string> = {
  'mailboxes:read': 'Read your mailboxes and their status',
  'mailboxes:write': 'Connect, pause, and remove your mailboxes',
  'campaigns:read': 'Read your campaigns and their sequences',
  'campaigns:write': 'Create and edit your campaigns',
  'contacts:read': 'Read your contacts',
  'contacts:write': 'Add and edit your contacts',
  'lists:read': 'Read your contact lists',
  'lists:write': 'Create and edit your contact lists',
  'crm:read': 'Read your CRM companies, deals, pipelines, activities, and threads',
  'crm:write': 'Create and edit your CRM companies, deals, and pipelines',
  'inbox:write': 'Mark your inbox threads as read or unread',
}

/** A human-readable consent sentence for a scope, or the raw value if unmapped. */
export function scopeConsentLabel(scope: string): string {
  return SCOPE_CONSENT_LABELS[scope] ?? scope
}
