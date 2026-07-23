import { httpStatus } from '@/lib/rtk-error'

/** Distinguishes a mis-configured server from a transient start failure. */
export type StartErrorKind = 'disabled' | 'generic'

/** Copy for the Gmail-connect "start" error banner, keyed by failure kind. */
export const startErrorCopy: Record<StartErrorKind, string> = {
  disabled: "Gmail connect isn't configured on this server.",
  generic: "Couldn't start Google sign-in — try again.",
}

/**
 * Maps an RTK Query error from `POST /mailboxes/oauth/google/start` to a banner
 * kind. A 501 means the server has no Gmail OAuth credentials configured
 * ("disabled"); anything else — another HTTP status, a network error, or an
 * absent error — is treated as a transient failure ("generic").
 */
export function startErrorKind(err: unknown): StartErrorKind {
  return httpStatus(err) === 501 ? 'disabled' : 'generic'
}
