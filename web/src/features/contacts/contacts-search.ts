// The contacts list's URL contract and the pure rules around it — parsing the
// search params, deciding what `q` is worth sending, the cursor stack that gives
// keyset pagination a Previous button, and the copy for counts and failures.
// Component-free so each rule is unit-tested directly and the page file only
// exports components (fast refresh).
import { httpStatus } from '@/lib/rtk-error'
import type { ContactPage, ContactSort } from '@/store/api'

/**
 * Shortest `q` the API accepts. Below two characters a trigram index can't be
 * selective, so the server answers 422 rather than pretending to search — the
 * UI holds the request back instead of provoking that error on every first
 * keystroke.
 */
export const MIN_QUERY_LENGTH = 2

export const DEFAULT_PAGE_SIZE = 50

/** The page sizes offered. The API rejects anything outside 1..100. */
export const PAGE_SIZES = [25, 50, 100] as const

export const CONTACT_SORTS: readonly { id: ContactSort; label: string }[] = [
  { id: 'newest', label: 'Newest first' },
  { id: 'oldest', label: 'Oldest first' },
  { id: 'email', label: 'Email A–Z' },
]

const DEFAULT_SORT: ContactSort = 'newest'

/**
 * The contacts view, as held in the URL: which list (omitted = every contact in
 * the workspace), the query, the ordering, the keyset cursor, and the page size.
 * All of it lives in the address bar so a search is shareable, Back works, and a
 * reload lands you where you were.
 */
export interface ContactsSearch {
  list?: string
  q?: string
  sort?: ContactSort
  cursor?: string
  limit?: number
}

/**
 * Narrow untrusted URL input to the contract. TanStack's search parser hands
 * back whatever was in the address bar — a hand-edited `?limit=9999` or
 * `?sort=nonsense` — so anything unrecognised falls back to the default rather
 * than being forwarded to the API for a 422.
 *
 * `cursor` is the deliberate exception: it is opaque, only the server can judge
 * it, and a cursor that is silently dropped here would look like a page that
 * mysteriously reset. It is passed through and the 400 is handled in the UI.
 */
export function parseContactsSearch(search: Record<string, unknown>): ContactsSearch {
  return {
    list: text(search.list),
    q: text(search.q),
    sort: CONTACT_SORTS.some((s) => s.id === search.sort) ? (search.sort as ContactSort) : undefined,
    cursor: text(search.cursor),
    limit: pageSize(search.limit),
  }
}

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : undefined
}

/** A page size is a number in the URL, but a hand-typed one arrives as a string. */
function pageSize(value: unknown): number | undefined {
  const size = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return PAGE_SIZES.some((s) => s === size) ? size : undefined
}

export function sortOrDefault(sort: ContactSort | undefined): ContactSort {
  return sort ?? DEFAULT_SORT
}

export function limitOrDefault(limit: number | undefined): number {
  return limit ?? DEFAULT_PAGE_SIZE
}

/**
 * The `q` actually sent to the API: trimmed, and only once it is long enough to
 * search. A shorter value still echoes in the field — the user is mid-word, not
 * wrong — it simply isn't a request yet.
 */
export function queryParam(typed: string): string | undefined {
  const trimmed = typed.trim()
  return trimmed.length >= MIN_QUERY_LENGTH ? trimmed : undefined
}

/** True while the box holds something too short to search — the cue to explain why. */
export function isTooShort(typed: string): boolean {
  const trimmed = typed.trim()
  return trimmed.length > 0 && trimmed.length < MIN_QUERY_LENGTH
}

/**
 * Keyset pagination knows the next page and the one it came from, but not
 * "page N back" — so the pages already visited are stacked as they are left.
 * The empty string stands for the first page, which has no cursor.
 */
export type CursorStack = readonly string[]

export function pushCursor(stack: CursorStack, current: string | undefined): CursorStack {
  return [...stack, current ?? '']
}

/** Pops the page to return to. An empty stack yields `undefined` — the first page. */
export function popCursor(stack: CursorStack): { stack: CursorStack; cursor: string | undefined } {
  return { stack: stack.slice(0, -1), cursor: stack[stack.length - 1] || undefined }
}

/**
 * "1–50 of 2,340" — the position in the result set, plus the size of it.
 *
 * The first row's number comes from how many pages were walked to get here, so
 * it is only knowable when this page was reached by paging. Opening a link that
 * already carries a cursor gives no such history — `pagesWalked` is `null` — and
 * the label says what it can substantiate ("50 of 2,340") rather than inventing
 * an offset.
 *
 * A capped total renders "10,000+": counting past the cap would be an unbounded
 * scan, so the number is a floor and is shown as one.
 */
export function rangeLabel(page: ContactPage, pagesWalked: number | null, limit: number): string {
  const count = page.items.length
  const total = `${page.total.toLocaleString()}${page.total_is_capped ? '+' : ''}`
  if (count === 0) return `0 of ${total}`
  if (pagesWalked === null) return `${count.toLocaleString()} of ${total}`
  const first = pagesWalked * limit + 1
  return `${first.toLocaleString()}–${(first + count - 1).toLocaleString()} of ${total}`
}

/**
 * A cursor the server won't accept — it was minted for another sort, or the
 * encoding moved on. The page recovers by dropping it and reloading the first
 * page, because a dead-ended list with no way back is worse than losing your
 * place.
 */
export function isStaleCursorError(error: unknown): boolean {
  return httpStatus(error) === 400
}

/** Human copy for a failed contacts load, by cause rather than a bare status. */
export function contactsErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === 422) return "That search wasn't accepted — try different terms."
  if (status === 404) return 'That list no longer exists.'
  if (status === 403) return "You don't have access to these contacts."
  return `Couldn't load contacts${status ? ` (${status})` : ''} — try again.`
}
