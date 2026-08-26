// The inbox list's URL contract and the pure rules around it — parsing the
// search params and packing/unpacking the two-part keyset cursor the API
// actually takes (before_last_message_at + before_id). Mirrors
// features/contacts/contacts-search.ts's shape (component-free, so each rule
// is unit-tested directly and the page file only exports components for fast
// refresh), adapted to what /inbox/threads' response shape actually is: no
// `total` (see rangeLabel's absence below — there is nothing to render one
// from), so pagination goes on the one fact a page size proves.
import { httpStatus } from '@/lib/rtk-error'
import type { ListInboxThreadsApiArg } from '@/store/api'

/**
 * The inbox view, as held in the URL: which mailbox it's scoped to (omitted =
 * every mailbox), which reply class, a free-text search (server-side,
 * case-insensitive substring match against subject or the linked contact's
 * email — real, workspace-wide, not just the loaded page), and the keyset
 * cursor.
 */
export interface InboxSearch {
  mailbox?: string
  class?: string
  q?: string
  cursor?: string
  scope?: InboxScope
}

/**
 * The rail's virtual folders, in render order — a local tuple rather than the
 * generated union because the order and the labels are ours, not the API's.
 *
 * `satisfies` pins every entry to a scope the generated client accepts, so a
 * scope removed from openapi.yaml fails `tsc` here rather than 400-ing at
 * runtime. The reverse direction (a scope the API gained but the rail omits)
 * is covered by SCOPE_LABELS' exhaustive Record below.
 */
export const INBOX_SCOPES = [
  'all',
  'unread',
  'today',
  'this_week',
  'awaiting_reply',
] as const satisfies readonly NonNullable<ListInboxThreadsApiArg['scope']>[]

export type InboxScope = (typeof INBOX_SCOPES)[number]

/**
 * The rail's label per scope. A `Record` over the generated union rather than
 * over the local one: adding a scope to openapi.yaml then fails `tsc` here
 * until it is labelled, so the rail can never silently omit a folder the API
 * has started serving.
 */
export const SCOPE_LABELS: Record<NonNullable<ListInboxThreadsApiArg['scope']>, string> = {
  all: 'All mail',
  unread: 'Unread',
  today: 'Today',
  this_week: 'This week',
  awaiting_reply: 'Awaiting reply',
}

/**
 * Narrow untrusted URL input to the contract. A hand-edited or stale param
 * that doesn't parse is dropped rather than forwarded to the API.
 */
export function parseInboxSearch(search: Record<string, unknown>): InboxSearch {
  return {
    mailbox: text(search.mailbox),
    class: text(search.class),
    q: text(search.q),
    cursor: text(search.cursor),
    scope: scope(search.scope),
  }
}

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : undefined
}

/**
 * An unrecognised scope is dropped rather than forwarded: the API answers 400
 * for one, and a hand-edited URL should degrade to the whole inbox instead of
 * dead-ending the page on an error the user can't act on.
 */
function scope(value: unknown): InboxScope | undefined {
  return INBOX_SCOPES.find((s) => s === value)
}

/**
 * The viewer's UTC offset in minutes East of UTC, which is what the API's
 * `tz_offset` takes. `getTimezoneOffset()` reports the opposite sign (minutes
 * to ADD to local time to reach UTC), hence the negation.
 *
 * Read at call time rather than module load so a long-lived tab that crosses a
 * DST boundary sends the offset in force now, not the one at page load.
 */
export function timezoneOffsetMinutes(): number {
  return -new Date().getTimezoneOffset()
}

/**
 * The scopes whose boundaries depend on the viewer's calendar, and so the only
 * list requests that need `tz_offset`. Sending it on the others would be pure
 * RTK Query cache-key noise, and would make a DST transition invalidate cached
 * pages the offset cannot affect.
 */
const TIMEZONE_DEPENDENT_SCOPES: readonly InboxScope[] = ['today', 'this_week']

/** The `tz_offset` to send for a scope, or `undefined` when it is irrelevant. */
export function scopeTimezoneOffset(forScope: InboxScope): number | undefined {
  return TIMEZONE_DEPENDENT_SCOPES.includes(forScope) ? timezoneOffsetMinutes() : undefined
}

/**
 * The API's keyset cursor is two values, `before_last_message_at` and
 * `before_id`, that must travel together (the OpenAPI description is
 * explicit: "must be set together... or not at all") — taken straight from
 * the last item of whatever page is on screen, no separate next_cursor field
 * needed. This packs the pair into the URL's one opaque `cursor` param and
 * unpacks it again. Neither an ISO timestamp nor a UUID can contain `::`, so
 * a plain split is safe.
 */
const CURSOR_SEPARATOR = '::'

export function encodeCursor(lastMessageAt: string, id: string): string {
  return `${lastMessageAt}${CURSOR_SEPARATOR}${id}`
}

export interface DecodedCursor {
  beforeLastMessageAt: string
  beforeId: string
}

/** Returns `undefined` for a missing, malformed, or half-set cursor. */
export function decodeCursor(cursor: string | undefined): DecodedCursor | undefined {
  if (!cursor) return undefined
  const separatorIndex = cursor.indexOf(CURSOR_SEPARATOR)
  if (separatorIndex === -1) return undefined
  const beforeLastMessageAt = cursor.slice(0, separatorIndex)
  const beforeId = cursor.slice(separatorIndex + CURSOR_SEPARATOR.length)
  if (!beforeLastMessageAt || !beforeId) return undefined
  return { beforeLastMessageAt, beforeId }
}

/**
 * Keyset pagination knows the next page but not "page N back", so the pages
 * already visited are stacked as they're left — same reasoning as
 * `features/contacts/contacts-search.ts`'s `CursorStack`, duplicated rather
 * than imported (features never import each other, and the two lists' cursor
 * encodings differ: contacts' is an opaque server token, this one is the
 * packed pair above). The empty string stands for the first page.
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
 * A cursor the server won't accept — minted for a filter combination that's
 * since changed, or just stale. `/inbox/threads` answers 400 for a malformed
 * or half-set keyset cursor (see the OpenAPI description); the page recovers
 * by dropping it and reloading the first page rather than dead-ending.
 */
export function isStaleCursorError(error: unknown): boolean {
  return httpStatus(error) === 400
}

/** Human copy for a failed inbox load, by cause rather than a bare status. */
export function inboxErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  return `Couldn't load the inbox${status ? ` (${status})` : ''} — try again.`
}
