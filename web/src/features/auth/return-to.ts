/**
 * A `return_to` is only honoured when it resolves to a same-origin target. We
 * resolve it against `window.location.origin` and compare origins, then return
 * the NORMALIZED same-origin path (`pathname + search + hash`) — never the raw
 * string. This blocks the login screen from being turned into an open redirect:
 * a prefix check isn't enough (`/\evil.com` and `/\/evil.com` pass a `//` guard
 * but WHATWG-normalize backslashes to `//`, so a browser would navigate to
 * `https://evil.com/`). Resolving-then-verifying rejects protocol-relative
 * (`//evil.com`), backslash (`/\evil.com`), absolute (`https://evil.com`), and
 * scheme (`javascript:`, `data:`) inputs — they resolve off-origin or throw —
 * while allowing a legitimate same-origin path including the API's
 * `/oauth2/authorize?...` resume. Returning `pathname + search + hash` strips any
 * authority so the validated path can't smuggle an off-origin target. Callers
 * then navigate via `window.location.assign`, because the target may be the API's
 * `/oauth2/authorize` (not an SPA route) as well as an SPA path.
 *
 * Shared by the two places that hand a `return_to` to the browser or to the
 * server: the login form's post-login resume, and the Google sign-in start URL
 * (whose `return_to` comes back to us through the provider's callback, so it has
 * to be validated on the way out too).
 */
export function safeReturnTo(raw: string | undefined): string | null {
  if (!raw) return null
  try {
    const u = new URL(raw, window.location.origin)
    if (u.origin !== window.location.origin) return null
    return u.pathname + u.search + u.hash
  } catch {
    return null
  }
}
