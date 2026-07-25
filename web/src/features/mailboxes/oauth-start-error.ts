import { httpStatus } from '@/lib/rtk-error'

/**
 * The connect-flow / start-endpoint key for a one-click OAuth provider (drives
 * which `/oauth/<key>/start` mutation fires and the banner copy). Distinct from
 * the persisted mailbox `provider` value — Microsoft's connect key is
 * 'microsoft' but its stored provider is 'm365'.
 */
export type OauthProvider = 'gmail' | 'microsoft'

/** Distinguishes a mis-configured server from a transient start failure. */
export type StartErrorKind = 'disabled' | 'generic'

// Per-provider phrasing: the connect name (as shown in the dropdown) and the
// sign-in wording used in the transient-failure copy. Kept in one place so the
// two providers share a single banner-copy mapping rather than duplicating it.
const providerCopy: Record<OauthProvider, { connect: string; signIn: string }> = {
  gmail: { connect: 'Gmail', signIn: 'Google sign-in' },
  microsoft: { connect: 'Microsoft 365', signIn: 'Microsoft sign-in' },
}

/**
 * Copy for the OAuth-connect "start" error banner, keyed by failure kind, for
 * the given provider. `disabled` means the server has no OAuth credentials
 * configured for that provider; `generic` is any other transient failure.
 */
export function startErrorCopy(provider: OauthProvider): Record<StartErrorKind, string> {
  const { connect, signIn } = providerCopy[provider]
  return {
    disabled: `${connect} connect isn't configured on this server.`,
    generic: `Couldn't start ${signIn} — try again.`,
  }
}

/**
 * Maps an RTK Query error from a `POST /mailboxes/oauth/<provider>/start` to a
 * banner kind. A 501 means the server has no OAuth credentials configured for
 * that provider ("disabled"); anything else — another HTTP status, a network
 * error, or an absent error — is treated as a transient failure ("generic").
 */
export function startErrorKind(err: unknown): StartErrorKind {
  return httpStatus(err) === 501 ? 'disabled' : 'generic'
}
