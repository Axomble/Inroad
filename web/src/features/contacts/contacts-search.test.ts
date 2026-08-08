import { expect, test } from 'vitest'
import type { ContactPage } from '@/store/api'
import {
  contactsErrorMessage,
  isStaleCursorError,
  isTooShort,
  limitOrDefault,
  parseContactsSearch,
  popCursor,
  pushCursor,
  queryParam,
  rangeLabel,
  sortOrDefault,
} from './contacts-search'

// The URL is the contacts view's source of truth, so everything it can contain —
// including whatever a user hand-types into the address bar — has to land on a
// request the API will accept, or the page 422s on arrival.

test('parses a well-formed URL into the search contract', () => {
  expect(parseContactsSearch({ list: 'l-1', q: 'acme', sort: 'email', cursor: 'c1', limit: 25 })).toEqual({
    list: 'l-1',
    q: 'acme',
    sort: 'email',
    cursor: 'c1',
    limit: 25,
  })
})

test('still parses the legacy ?contact= param, which the route redirects on', () => {
  // Agent conversations already in the database link to `?contact=<id>`. The
  // validator has to return the param for `beforeLoad` to see it at all — a param
  // this parser drops is stripped before the route ever runs.
  expect(parseContactsSearch({ contact: 'c-1' }).contact).toBe('c-1')
  expect(parseContactsSearch({ contact: '' }).contact).toBeUndefined()
})

test('drops params the API would reject, so a hand-edited URL falls back to defaults', () => {
  const parsed = parseContactsSearch({ sort: 'salary', limit: 9999, q: '', list: 42 })
  expect(parsed.sort).toBeUndefined()
  expect(parsed.limit).toBeUndefined()
  expect(parsed.q).toBeUndefined()
  expect(parsed.list).toBeUndefined()
  expect(sortOrDefault(parsed.sort)).toBe('newest')
  expect(limitOrDefault(parsed.limit)).toBe(50)
})

test('accepts a page size that arrived as a string, and rejects one off the list', () => {
  expect(parseContactsSearch({ limit: '100' }).limit).toBe(100)
  expect(parseContactsSearch({ limit: '75' }).limit).toBeUndefined()
})

test('passes an unrecognisable cursor through — only the server can judge it', () => {
  // Dropping it here would look like a page that silently reset; the 400 is
  // handled explicitly instead.
  expect(parseContactsSearch({ cursor: 'not-really-base64' }).cursor).toBe('not-really-base64')
})

test('a query is only sent once it is long enough to search', () => {
  expect(queryParam('a')).toBeUndefined()
  expect(queryParam('  ')).toBeUndefined()
  expect(queryParam('  acme ')).toBe('acme')
  expect(isTooShort('a')).toBe(true)
  expect(isTooShort('')).toBe(false)
  expect(isTooShort('ac')).toBe(false)
})

test('the cursor stack walks forward and back, with the first page as the floor', () => {
  let stack = pushCursor([], undefined) // leaving page 1
  expect(stack).toEqual([''])
  stack = pushCursor(stack, 'c1') // leaving page 2
  expect(stack).toEqual(['', 'c1'])

  const back = popCursor(stack)
  expect(back.cursor).toBe('c1')
  expect(back.stack).toEqual([''])

  const home = popCursor(back.stack)
  expect(home.cursor).toBeUndefined() // '' is the cursor-less first page
  expect(home.stack).toEqual([])

  // Popping an empty stack is not an error: a pasted deep link has no history.
  expect(popCursor([]).cursor).toBeUndefined()
})

function page(overrides: Partial<ContactPage> = {}): ContactPage {
  return {
    items: Array.from({ length: 50 }, (_, i) => ({ id: `c-${i}`, email: `c${i}@acme.test` })),
    next_cursor: null,
    prev_cursor: null,
    total: 2340,
    total_is_capped: false,
    ...overrides,
  }
}

test('the range label counts from the pages walked to get here', () => {
  expect(rangeLabel(page(), 0, 50)).toBe('1–50 of 2,340')
  expect(rangeLabel(page(), 2, 50)).toBe('101–150 of 2,340')
})

test('a capped total renders as a floor, not a fact', () => {
  expect(rangeLabel(page({ total: 10000, total_is_capped: true }), 0, 50)).toBe('1–50 of 10,000+')
})

test('a deep link with no page history claims a count, not an offset it cannot know', () => {
  // Reached by pasting a URL that already carries a cursor: the page size is
  // known, the position in the result set is not.
  expect(rangeLabel(page(), null, 50)).toBe('50 of 2,340')
})

test('an empty result still reports the total it matched', () => {
  expect(rangeLabel(page({ items: [], total: 0 }), 0, 50)).toBe('0 of 0')
})

test('error copy names the cause instead of leaking a status code', () => {
  const status = (code: number) => ({ status: code, data: {} })
  expect(isStaleCursorError(status(400))).toBe(true)
  expect(isStaleCursorError(status(422))).toBe(false)
  expect(contactsErrorMessage(status(422))).toMatch(/wasn't accepted/)
  expect(contactsErrorMessage(status(404))).toMatch(/no longer exists/)
  expect(contactsErrorMessage(status(403))).toMatch(/don't have access/)
  expect(contactsErrorMessage(status(500))).toMatch(/\(500\)/)
  expect(contactsErrorMessage({ name: 'Error', message: 'offline' })).toBe("Couldn't load contacts — try again.")
})
