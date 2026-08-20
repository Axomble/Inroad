import { httpStatus, isFetchBaseQueryError, parseRetryAfter, retryAfterSeconds, withRetryAfter } from '../rtk-error'

/**
 * A rate-limited error in the shape a component actually receives: RTK hands
 * callers the error payload alone, so `{status, data}` plus whatever the base
 * query folded on — never a `meta`. `store/empty-api.test.ts` proves the fold
 * happens for a real request; this file covers the pieces in isolation.
 */
function rateLimited(retryAfter?: number): unknown {
  return retryAfter === undefined ? { status: 429, data: undefined } : { status: 429, data: undefined, retryAfter }
}

function responseWithRetryAfter(value: string | null): Response {
  const headers = new Headers()
  if (value !== null) headers.set('retry-after', value)
  return new Response(null, { status: 429, headers })
}

test('isFetchBaseQueryError narrows objects carrying a status', () => {
  expect(isFetchBaseQueryError({ status: 404, data: undefined })).toBe(true)
  expect(isFetchBaseQueryError({ status: 'FETCH_ERROR', error: 'x' })).toBe(true)
  expect(isFetchBaseQueryError({ message: 'plain error' })).toBe(false)
  expect(isFetchBaseQueryError(null)).toBe(false)
  expect(isFetchBaseQueryError(undefined)).toBe(false)
})

test('httpStatus returns the numeric HTTP status only', () => {
  expect(httpStatus({ status: 501, data: undefined })).toBe(501)
  expect(httpStatus({ status: 'TIMEOUT_ERROR', error: 'x' })).toBeUndefined()
  expect(httpStatus({ name: 'Error', message: 'boom' })).toBeUndefined()
  expect(httpStatus(undefined)).toBeUndefined()
})

test('parseRetryAfter reads a delay in seconds', () => {
  expect(parseRetryAfter('120')).toBe(120)
  expect(parseRetryAfter('0')).toBe(0)
  // Negative or fractional values still have to yield a usable whole delay.
  expect(parseRetryAfter('-5')).toBe(0)
  expect(parseRetryAfter('1.6')).toBe(2)
})

test('parseRetryAfter converts an HTTP date into a delay', () => {
  const seconds = parseRetryAfter(new Date(Date.now() + 90_000).toUTCString())
  // Allow a little slack for the clock ticking between construction and read.
  expect(seconds).toBeGreaterThanOrEqual(88)
  expect(seconds).toBeLessThanOrEqual(90)
})

test('parseRetryAfter returns null for an absent or unparseable header', () => {
  expect(parseRetryAfter(null)).toBeNull()
  expect(parseRetryAfter(undefined)).toBeNull()
  expect(parseRetryAfter('')).toBeNull()
  expect(parseRetryAfter('soon please')).toBeNull()
})

test('withRetryAfter folds the header onto the error, and leaves it alone without one', () => {
  const error = { status: 429, data: { error: 'rate_limited' } } as const

  expect(withRetryAfter(error, responseWithRetryAfter('120'))).toEqual({ ...error, retryAfter: 120 })
  // No header, no response at all (a transport failure): same error back.
  expect(withRetryAfter(error, responseWithRetryAfter(null))).toBe(error)
  expect(withRetryAfter(error, undefined)).toBe(error)
})

test('retryAfterSeconds reads the delay off the error a component receives', () => {
  expect(retryAfterSeconds(rateLimited(120))).toBe(120)
  expect(retryAfterSeconds(rateLimited(0))).toBe(0)
})

test('retryAfterSeconds returns null when the error carries no delay', () => {
  expect(retryAfterSeconds(rateLimited())).toBeNull()
  expect(retryAfterSeconds({ name: 'Error', message: 'boom' })).toBeNull()
  expect(retryAfterSeconds(undefined)).toBeNull()
  // The pre-fix fixture: a `meta`-carrying error. No component is ever handed
  // one, and reading the header from there is exactly what silently failed —
  // so this must NOT be treated as a source of the delay.
  const headers = new Headers({ 'retry-after': '120' })
  expect(retryAfterSeconds({ status: 429, data: undefined, meta: { response: { headers } } })).toBeNull()
})
