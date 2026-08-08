// The one client-side source of truth for "may this account do the things the
// server gates behind a confirmed email address?". Component-free on purpose:
// other features import this module read-only (the established cross-feature
// hook-reuse exception — see mailboxes-page.tsx pulling the warmup query),
// while the gated control itself is the generic
// `components/shared/gated-action-button.tsx`, so no feature imports another
// feature's UI.
import { useAppSelector } from '@/store/hooks'
import { useAuthMeQuery } from './api'

/**
 * Whether the signed-in user's email address is confirmed, read from
 * `/auth/me` (verification isn't in the JWT — `auth.RequireVerified` checks it
 * fresh by DB lookup, so the session tells us nothing). Shares the
 * `Session:CURRENT` cache entry with `UnverifiedBanner`, so gating a control
 * costs no extra request, and the base query's invalidation on a 403
 * `email_not_verified` re-resolves both at once.
 *
 * `verified` is optimistically `true` while the answer is unknown (signed out,
 * or the query still in flight): disabling a control on an answer we don't have
 * yet would gate users the server would have let through. This is a UX
 * affordance only — the server check remains the authority, which is why every
 * gated action still maps a 403 to its own error copy.
 */
export function useEmailVerified(): { verified: boolean } {
  const authed = useAppSelector((s) => s.auth.status === 'authed')
  const { data } = useAuthMeQuery(undefined, { skip: !authed })
  return { verified: data?.email_verified !== false }
}

/**
 * The single phrasing for a blocked-by-verification action, shared by the
 * disabled control's explanation and by the error copy shown if the action is
 * attempted anyway and the server answers 403. `action` completes the sentence:
 * `emailVerificationHint('connect a mailbox')`.
 */
export function emailVerificationHint(action: string): string {
  return `Verify your email address to ${action}. Use “Resend email” in the banner at the top of the page to get a new link.`
}
