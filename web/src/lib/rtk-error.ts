import type { FetchBaseQueryError } from '@reduxjs/toolkit/query/react'

/**
 * Typed RTK Query error helpers. RTK mutation/query results carry an `error`
 * that is either a `FetchBaseQueryError` (HTTP/transport) or a
 * `SerializedError` (a thrown JS error). These narrow that `unknown` without
 * the loose `'status' in err` checks scattered across components.
 */

/** Narrows an unknown RTK Query error to a `FetchBaseQueryError`. */
export function isFetchBaseQueryError(err: unknown): err is FetchBaseQueryError {
  return typeof err === 'object' && err !== null && 'status' in err
}

/**
 * The numeric HTTP status from an RTK Query error, or `undefined` when the
 * error carries a string status tag (`FETCH_ERROR`, `TIMEOUT_ERROR`, …) or is
 * a serialized/absent error.
 */
export function httpStatus(err: unknown): number | undefined {
  if (isFetchBaseQueryError(err) && typeof err.status === 'number') return err.status
  return undefined
}

/**
 * The server's own explanation of a failure, when it sent one worth showing.
 * The API's error envelope is `{"error": msg}` (see `httpx.Error`), and some
 * handlers answer `{"message": …}` instead, so both are read. Machine codes
 * like `email_not_verified` are filtered out: a token with no spaces reads as
 * noise inside a sentence, while prose ("currency must be a three-letter ISO
 * code") is exactly what the user needs.
 */
export function serverDetail(err: unknown): string | undefined {
  if (!isFetchBaseQueryError(err)) return undefined
  const { data } = err
  if (typeof data === 'string' && data.trim()) return data.trim()
  if (typeof data !== 'object' || data === null) return undefined
  const body = data as { message?: unknown; error?: unknown }
  for (const value of [body.message, body.error]) {
    if (typeof value === 'string' && value.trim() && value.includes(' ')) return value.trim()
  }
  return undefined
}

/**
 * An RTK error once the base query has folded a `Retry-After` delay onto it.
 *
 * The header lives on the raw `Response`, which `fetchBaseQuery` stashes on the
 * base query result's `meta` — and `meta` is attached to the dispatched thunk
 * action, *not* to the error payload. A component's `error` is therefore only
 * ever `{status, data}`, so the delay has to be copied into the payload at the
 * one seam every request already passes through (`store/empty-api.ts`).
 */
export type FetchErrorWithRetryAfter = FetchBaseQueryError & { retryAfter?: number }

/**
 * Parses a `Retry-After` header value into whole seconds, accepting both forms
 * the spec allows: a delay in seconds (`"120"`) or an HTTP date
 * (`"Wed, 21 Oct 2026 07:28:00 GMT"`). `null` when absent or unparseable.
 */
export function parseRetryAfter(header: string | null | undefined): number | null {
  if (!header) return null
  const asNumber = Number(header)
  if (Number.isFinite(asNumber)) return Math.max(0, Math.round(asNumber))
  const asDate = new Date(header).getTime()
  if (Number.isFinite(asDate)) return Math.max(0, Math.round((asDate - Date.now()) / 1000))
  return null
}

/**
 * Copies the response's `Retry-After` delay onto the error payload so it
 * survives the trip to a component. Called once, by the shared base query;
 * returns the error untouched when the server sent no usable header.
 */
export function withRetryAfter<E extends FetchBaseQueryError>(error: E, response?: Response): E {
  const seconds = parseRetryAfter(response?.headers.get('retry-after'))
  if (seconds === null) return error
  return { ...error, retryAfter: seconds }
}

/**
 * The `Retry-After` delay (in seconds) a rate-limited request came back with,
 * or `null` when the server sent none. Reads the field the shared base query
 * folded on via `withRetryAfter` — callers get this through the one error seam
 * rather than reaching for `meta`, which never reaches them anyway.
 */
export function retryAfterSeconds(err: unknown): number | null {
  if (!isFetchBaseQueryError(err)) return null
  const { retryAfter } = err as FetchErrorWithRetryAfter
  return typeof retryAfter === 'number' ? retryAfter : null
}
