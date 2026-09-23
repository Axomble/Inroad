// The inbox list's URL contract and the pure rules around it — parsing the
// search params and packing/unpacking the two-part keyset cursor the API
// actually takes (before_last_message_at + before_id). Mirrors
// features/contacts/contacts-search.ts's shape (component-free, so each rule
// is unit-tested directly and the page file only exports components for fast
// refresh), adapted to what /inbox/threads' response shape actually is: no
// `total` (see rangeLabel's absence below — there is nothing to render one
// from), so pagination goes on the one fact a page size proves.
import { httpStatus, retryAfterSeconds, serverDetail } from '@/lib/rtk-error'
import type { InboxSearchHit, ListInboxThreadsApiArg } from '@/store/api'

/**
 * The inbox view, as held in the URL: which mailbox it's scoped to (omitted =
 * every mailbox), which reply class, a full-text search (`q`, answered by
 * GET /inbox/search over subjects and bodies on both legs of every thread —
 * workspace-wide, not just the loaded page), and the thread list's keyset
 * cursor. `q` is in the URL so a search is linkable and survives a reload;
 * its "Load more" pages are not, because they are this tab's scroll position.
 */
export interface InboxSearch {
  mailbox?: string
  class?: string
  q?: string
  cursor?: string
  scope?: InboxScope
  /** An operator-assigned label's id — the rail's label scope. */
  label?: string
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
  'snoozed',
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
  snoozed: 'Snoozed',
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
    label: text(search.label),
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
 * a plain split is safe. The stack of already-visited cursors behind the
 * Previous button is not this module's business: `@/lib/cursor-stack` stores
 * whatever string it is handed and never interprets one.
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

/**
 * The search endpoint's cap on the trimmed query, in characters (code points,
 * which is what the server counts). Mirrored here so an over-long paste is
 * explained before a request is spent on a guaranteed 400.
 */
export const SEARCH_QUERY_MAX_LENGTH = 256

/** True when a (trimmed) query is longer than the server will accept. */
export function isSearchQueryTooLong(query: string): boolean {
  return [...query].length > SEARCH_QUERY_MAX_LENGTH
}

/**
 * The server's 400 for a query with nothing to look for ("the", "-foo"). The
 * API has no machine code for it, so it is recognised by its prose; if that
 * wording ever changes this degrades to the generic 400 copy, which still
 * shows the server's own message.
 */
const NOT_SELECTIVE_DETAIL = /word to look for/i

/** Queries shorter than this are matched against message text only, never addresses. */
export const ADDRESS_MATCH_MIN_LENGTH = 3

export interface SearchErrorCopy {
  title: string
  description: string
  /** Whether re-sending the same query can succeed; otherwise the query must change. */
  retryable: boolean
}

/**
 * Human copy for a failed search, by cause. The 400/422 family is about the
 * query itself, so the copy says how to change it and retrying is not offered;
 * throttling, a timeout and outages are worth retrying.
 */
export function searchErrorCopy(error: unknown): SearchErrorCopy {
  const status = httpStatus(error)
  const detail = serverDetail(error)
  switch (status) {
    case 400:
      if (detail && NOT_SELECTIVE_DETAIL.test(detail)) {
        return {
          title: 'Add a word to search for',
          description:
            'Common words like “the” and exclusions like “-foo” can’t be searched on their own — add a more distinctive word.',
          retryable: false,
        }
      }
      return {
        title: "That search can't run",
        description: `Edit the search to try again.${detail ? ` (Server said: ${detail})` : ''}`,
        retryable: false,
      }
    case 422:
      return {
        title: 'That search matches too much',
        description: 'It matches more than 10,000 messages or contacts — add words to narrow it.',
        retryable: false,
      }
    case 429: {
      const seconds = retryAfterSeconds(error)
      return {
        title: 'Too many searches',
        description: seconds
          ? `Searches are being limited — try again in ${seconds} ${seconds === 1 ? 'second' : 'seconds'}.`
          : 'Searches are being limited — wait a moment and try again.',
        retryable: true,
      }
    }
    case 503:
      return {
        title: 'That search took too long',
        description: 'The server stopped it before it finished — a more specific search will likely work, or try again.',
        retryable: true,
      }
    default:
      return {
        title: "Couldn't search the inbox",
        description: `The search failed${status ? ` (${status})` : ''} — try again.`,
        retryable: true,
      }
  }
}

const MATCH_REASON_LABELS: Record<InboxSearchHit['matched_legs'][number], string> = {
  inbound: 'received',
  outbound: 'sent',
  contact: 'sender address',
}

/**
 * Why a hit matched, in the operator's words: "received" is text in the
 * contact's replies, "sent" is text in anything we sent (campaign steps and
 * manual replies), "sender address" is the query found in the contact's email
 * or an inbound From address. The API guarantees at least one reason, in that
 * order; a Record over the generated union means a new reason fails `tsc`
 * here until it is labelled.
 */
export function matchedLegsLabel(legs: InboxSearchHit['matched_legs']): string {
  const labels = legs.map((leg) => MATCH_REASON_LABELS[leg])
  if (labels.length <= 2) return labels.join(' & ')
  return `${labels.slice(0, -1).join(', ')} & ${labels[labels.length - 1]}`
}
