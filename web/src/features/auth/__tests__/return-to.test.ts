import { expect, test } from 'vitest'
import { safeReturnTo } from '../return-to'

// The shared same-origin guard for every `return_to` in the auth flows. It has two
// jobs, and the second one is easy to forget: refuse off-origin targets (an open
// redirect), and refuse anything the BACKEND's allowlist would refuse — because a
// value this guard silently repairs is one the server then drops, which loses the
// user's resume with nothing on screen to explain it.
//
// jsdom's origin here is http://localhost:5173 (see vitest.config.ts).

test.each([
  ['/app/mailboxes', '/app/mailboxes'],
  ['/', '/'],
  ['/oauth2/authorize?client_id=abc&state=xyz', '/oauth2/authorize?client_id=abc&state=xyz'],
  ['/app/inbox#thread-1', '/app/inbox#thread-1'],
  // Percent-encoded characters are legal in a path and survive untouched; it's raw
  // control bytes that are refused, not their encoded forms.
  ['/app/search?q=a%20b', '/app/search?q=a%20b'],
])('a same-origin path (%j) is honoured, normalized', (input, expected) => {
  expect(safeReturnTo(input)).toBe(expected)
})

// Every open-redirect family. `'/\\evil.com'` is the literal `/\evil.com`, which
// WHATWG-normalizes to `//evil.com` — the bug a naive `//` prefix check misses.
test.each([
  '//evil.com',
  '/\\evil.com',
  '/\\/evil.com',
  'https://evil.com',
  'http://evil.com/x',
  'javascript:alert(1)',
  'data:text/html,x',
  '\\\\evil.com',
])('an off-origin target (%j) is refused', (input) => {
  expect(safeReturnTo(input)).toBeNull()
})

// These are all same-origin once resolved, so the open-redirect check would pass
// them — but the backend drops each one, so honouring them would send a value that
// comes back as "no resume at all".
test.each([
  ['a CR/LF that could split a Location header server-side', '/app\r\nSet-Cookie: a=b'],
  ['a bare newline', '/app\nX: y'],
  ['a tab', '/app\tx'],
  ['a leading space that resolution would trim away', ' /app'],
  ['a schemeless relative path with no leading slash', 'app/relative'],
  ['a path longer than the 512-byte cap', '/' + 'a'.repeat(600)],
])('%s is refused rather than silently repaired', (_why, input) => {
  expect(safeReturnTo(input)).toBeNull()
})

test('nothing at all is not a resume', () => {
  expect(safeReturnTo(undefined)).toBeNull()
  expect(safeReturnTo('')).toBeNull()
})

test('a path at exactly the length cap is still honoured', () => {
  const path = '/' + 'a'.repeat(511)
  expect(path).toHaveLength(512)
  expect(safeReturnTo(path)).toBe(path)
})

// The backend rejects whitespace with Go's `unicode.IsSpace`, which is not ASCII-only.
// Each of these resolves to a same-origin path, so the open-redirect half of the guard
// passes them — and every one is dropped server-side, so honouring them would send a
// resume that silently never happens.
test.each([
  ['a non-breaking space', '/app/\u00A0inbox'],
  ['a line separator', '/app\u2028x'],
  ['an en quad', '/app\u2000x'],
  ['an ideographic space', '/app\u3000x'],
  ['NEL, which Go treats as space but JS `\\s` does not', '/app\u0085x'],
])('%s is refused, matching the backend', (_why, input) => {
  expect(safeReturnTo(input)).toBeNull()
})

// The cap is bytes on the server (`len()` on a Go string), so a character count is the
// wrong ruler: 400 three-byte characters is 1200 bytes and refused there.
test('the length cap counts UTF-8 bytes, not characters', () => {
  const path = '/' + '中'.repeat(400)
  expect(path.length).toBeLessThan(512)
  expect(new TextEncoder().encode(path).length).toBeGreaterThan(512)
  expect(safeReturnTo(path)).toBeNull()
})
