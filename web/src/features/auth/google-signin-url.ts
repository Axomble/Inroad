import { safeReturnTo } from './return-to'

/**
 * Where the browser goes to start Google sign-in.
 *
 * Unlike every other sign-in method in this app, Google does not resolve through
 * an RTK Query mutation: there is no JSON session body to receive. `GET` on the
 * start route 302s straight to Google's consent screen, and the callback sets the
 * refresh cookie server-side before redirecting back — so the SPA picks the
 * session up from a refresh at `/auth/google/callback` rather than from a response
 * body. That makes this a top-level browser navigation, and the only thing the
 * client owns is the URL.
 *
 * The base is resolved the same way the RTK base query resolves it
 * (`VITE_API_BASE_URL ?? '/api/v1'`, against the page origin), so a hoster who
 * points the SPA at a separate API host gets a start URL on that host too.
 *
 * `returnTo` rides along so the callback can resume whatever bounced the user to
 * the login screen. The server validates it as a same-origin path and remembers it
 * against the state nonce rather than echoing it through Google; we validate it
 * here as well, with the same guard the login form's resume uses, so a hostile
 * value never leaves this origin in the first place.
 *
 * There is a `POST` on the same path returning `{auth_url}` — it exists for the
 * invite-with-Google flow (an invite token is a bearer credential and must not sit
 * in a URL the browser records in history) and as a capability probe answering 501
 * when Google is unconfigured. Neither is used here: sign-in from the login and
 * register screens carries no invite token, and an unconfigured server 302s to
 * `/?google_error=disabled`, which the banner already explains. Probing on every
 * unauthenticated page view to pre-hide the button would cost a request on the
 * critical path to save a redirect that already lands somewhere sensible.
 */
export function googleSigninUrl(returnTo?: string): string {
  const apiBase = import.meta.env.VITE_API_BASE_URL ?? '/api/v1'
  const url = new URL(`${apiBase.replace(/\/$/, '')}/auth/oauth/google/start`, window.location.origin)
  const resume = safeReturnTo(returnTo)
  if (resume) url.searchParams.set('return_to', resume)
  return url.toString()
}
