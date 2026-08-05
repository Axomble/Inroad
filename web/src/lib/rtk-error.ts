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
 * The `Retry-After` value (in seconds) from a rate-limited RTK Query error, or
 * `null` when the header is absent/unparseable. The header rides on the raw
 * `Response` that `fetchBaseQuery` stashes on `error.meta` — this encapsulates
 * that guarded reach so callers inspect it through the one error seam rather
 * than an inline structural cast. Accepts both forms the spec allows: a delay
 * in seconds (`"120"`) or an HTTP date (`"Wed, 21 Oct 2026 07:28:00 GMT"`).
 */
export function retryAfterSeconds(err: unknown): number | null {
  if (!isFetchBaseQueryError(err) || !('meta' in err)) return null
  const meta = (err as { meta?: { response?: { headers?: Headers } } }).meta
  const header = meta?.response?.headers?.get('retry-after')
  if (!header) return null
  const asNumber = Number(header)
  if (Number.isFinite(asNumber)) return Math.max(0, Math.round(asNumber))
  const asDate = new Date(header).getTime()
  if (Number.isFinite(asDate)) return Math.max(0, Math.round((asDate - Date.now()) / 1000))
  return null
}
