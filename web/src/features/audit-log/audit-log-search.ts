// The audit log's URL contract and the translation from it to API arguments.
//
// The filters live in the address bar so an investigation is linkable ("every
// failed sign-in last Tuesday") and survives a reload. The pagination cursor
// deliberately does not: the list loads more by appending, so a cursor in the URL
// would reopen on page three with pages one and two missing, and the server binds
// each cursor to the filter set that minted it anyway.
import type { AuditActorType, ListAuditEventsApiArg } from '@/store/api'
import { isActionFilter, isActorType } from './audit-vocabulary'

export interface AuditLogSearch {
  /** A whole action or a category prefix — see `isActionFilter`. */
  action?: string
  actor?: AuditActorType
  /** First day shown, `YYYY-MM-DD`, in the reader's own timezone. */
  from?: string
  /** Last day shown, inclusive, `YYYY-MM-DD`. */
  to?: string
}

/** A partial update to the URL: a key set to undefined is removed. */
export type AuditSearchPatch = { [K in keyof AuditLogSearch]?: string | undefined }

export const CLEAR_FILTERS: AuditSearchPatch = { action: undefined, actor: undefined, from: undefined, to: undefined }

/** The API arguments a filter set produces — everything except the page. */
export type AuditFilterArgs = Pick<ListAuditEventsApiArg, 'action' | 'actorType' | 'since' | 'until'>

const DAY = /^\d{4}-\d{2}-\d{2}$/

/**
 * Narrows untrusted URL input to the contract. Anything unrecognised is dropped
 * rather than forwarded: the server answers a malformed filter with a 400, and a
 * hand-edited link should open the unfiltered log, not an error.
 */
export function parseAuditLogSearch(search: Record<string, unknown>): AuditLogSearch {
  const action = text(search.action)
  const actor = text(search.actor)
  return {
    action: action !== undefined && isActionFilter(action) ? action : undefined,
    actor: actor !== undefined && isActorType(actor) ? actor : undefined,
    from: day(search.from),
    to: day(search.to),
  }
}

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : undefined
}

function day(value: unknown): string | undefined {
  const raw = text(value)
  if (raw === undefined || !DAY.test(raw)) return undefined
  return startOfLocalDay(raw) === undefined ? undefined : raw
}

/**
 * Local midnight at the start of a `YYYY-MM-DD` day, or undefined for a date
 * that doesn't exist (`2026-02-30` passes the pattern and rolls over in `Date`).
 */
function startOfLocalDay(value: string, offsetDays = 0): Date | undefined {
  const [year, month, dayOfMonth] = value.split('-').map(Number)
  if (year === undefined || month === undefined || dayOfMonth === undefined) return undefined
  const date = new Date(year, month - 1, dayOfMonth)
  if (date.getFullYear() !== year || date.getMonth() !== month - 1 || date.getDate() !== dayOfMonth) {
    return undefined
  }
  date.setDate(date.getDate() + offsetDays)
  return date
}

/** True when the range is backwards — the server would refuse it with a 400. */
export function isRangeInverted(search: AuditLogSearch): boolean {
  return search.from !== undefined && search.to !== undefined && search.to < search.from
}

export function hasFilters(search: AuditLogSearch): boolean {
  return (
    search.action !== undefined || search.actor !== undefined || search.from !== undefined || search.to !== undefined
  )
}

/**
 * The API arguments for a filter set.
 *
 * Days become instants in the READER's timezone, because "events on the 3rd"
 * means the 3rd where the reader is. `to` is inclusive in the UI and the API's
 * `until` is exclusive, so it becomes the midnight that ENDS that day.
 */
export function auditFilterArgs(search: AuditLogSearch): AuditFilterArgs {
  const since = search.from === undefined ? undefined : startOfLocalDay(search.from)
  const until = search.to === undefined ? undefined : startOfLocalDay(search.to, 1)
  return {
    ...(search.action === undefined ? {} : { action: search.action }),
    ...(search.actor === undefined ? {} : { actorType: search.actor }),
    ...(since === undefined ? {} : { since: since.toISOString() }),
    ...(until === undefined ? {} : { until: until.toISOString() }),
  }
}
