/**
 * The longest `return_to` worth honouring, counted in UTF-8 BYTES because the
 * backend's cap is `len(path)` on a Go string — bytes, not characters. Measuring
 * `raw.length` here instead would accept a 512-character non-ASCII path that the
 * server then drops for being over 512 bytes, which is the failure this guard is
 * meant to prevent rather than cause.
 */
const MAX_BYTES = 512

/** Hoisted: constructing one per call would allocate on every keystroke-driven check. */
const utf8 = new TextEncoder()

/**
 * Control characters and whitespace, which the backend rejects outright because a
 * CR/LF in a value it echoes into a `Location` header could split the response.
 * That specific risk doesn't exist on this side — nothing here writes a header, and
 * WHATWG URL parsing strips these during normalization anyway. They're rejected
 * here so the client and server agree on what a valid resume is: silently
 * *repairing* a value the server will then drop is how a resume disappears with no
 * explanation, which is a worse bug to diagnose than a refused one.
 *
 * The pattern tracks Go's `unicode.IsSpace`, which is what the backend uses, so it
 * has to reach past ASCII: U+00A0, U+2028 and the other Unicode space separators
 * resolve to a perfectly same-origin path here but are refused there. `\s` supplies
 * most of them; U+0085 (NEL) is in Go's set but not in `\s`, so it is listed
 * explicitly. `\s` also matches U+FEFF, which Go's set does not — being stricter
 * than the server is safe in a way that being laxer is not, since a refused resume
 * falls back to the default landing route and one that vanishes server-side looks
 * like the feature is broken.
 */
// oxlint-disable-next-line no-control-regex -- matching control characters is the point
const FORBIDDEN = /[\u0000-\u0020\u007F\u0085]|\s/u

/**
 * A `return_to` is only honoured when it is a same-origin path. Two stages, because
 * neither alone is enough.
 *
 * First the RAW string must look like a single-slash-rooted path with no control
 * characters and a sane length. This is what keeps the guard in step with the
 * backend's own allowlist (`identity.safeReturnTo`): resolution below would happily
 * *normalize* a leading space, a tab, or a schemeless `app/relative` into something
 * same-origin, and then the server would drop the value anyway — so the user would
 * lose their resume with nothing to show why.
 *
 * Then it must actually resolve to this origin. A prefix check isn't enough
 * (`/\evil.com` and `/\/evil.com` pass a `//` guard but WHATWG-normalize backslashes
 * to `//`, so a browser would navigate to `https://evil.com/`). Resolving-then-
 * verifying rejects protocol-relative (`//evil.com`), backslash (`/\evil.com`),
 * absolute (`https://evil.com`), and scheme (`javascript:`, `data:`) inputs — they
 * resolve off-origin or throw — while allowing a legitimate same-origin path
 * including the API's `/oauth2/authorize?...` resume. Returning
 * `pathname + search + hash` (never the raw string) strips any authority, so a
 * validated path can't smuggle an off-origin target. Callers then navigate via
 * `window.location.assign`, because the target may be the API's `/oauth2/authorize`
 * rather than an SPA route.
 *
 * Shared by the three places a `return_to` is handed to the browser or the server:
 * the login form's post-login resume, the Google sign-in start URL, and the Google
 * callback landing route — where it arrives back as a query param and so is
 * re-validated even though the server already checked it.
 */
export function safeReturnTo(raw: string | undefined): string | null {
  if (!raw || utf8.encode(raw).length > MAX_BYTES) return null
  if (FORBIDDEN.test(raw)) return null
  // Exactly one leading slash, so `//host` and `/\host` are gone before resolution.
  if (!raw.startsWith('/') || raw.startsWith('//') || raw.startsWith('/\\')) return null

  try {
    const url = new URL(raw, window.location.origin)
    if (url.origin !== window.location.origin) return null
    return url.pathname + url.search + url.hash
  } catch {
    return null
  }
}
