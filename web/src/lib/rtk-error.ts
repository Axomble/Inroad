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
