import { httpStatus, isFetchBaseQueryError, retryAfterSeconds } from './rtk-error'

function errWithRetryAfter(value: string | null): unknown {
  const headers = new Headers()
  if (value !== null) headers.set('retry-after', value)
  return { status: 429, data: undefined, meta: { response: { headers } } }
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

test('retryAfterSeconds reads a numeric Retry-After delay', () => {
  expect(retryAfterSeconds(errWithRetryAfter('120'))).toBe(120)
  expect(retryAfterSeconds(errWithRetryAfter('0'))).toBe(0)
})

test('retryAfterSeconds parses an HTTP-date Retry-After into a delay', () => {
  const in90s = new Date(Date.now() + 90_000).toUTCString()
  const seconds = retryAfterSeconds(errWithRetryAfter(in90s))
  expect(seconds).not.toBeNull()
  // Allow a little slack for the clock ticking between construction and read.
  expect(seconds).toBeGreaterThanOrEqual(88)
  expect(seconds).toBeLessThanOrEqual(90)
})

test('retryAfterSeconds returns null when the header or meta is absent', () => {
  expect(retryAfterSeconds(errWithRetryAfter(null))).toBeNull()
  expect(retryAfterSeconds({ status: 429, data: undefined })).toBeNull()
  expect(retryAfterSeconds({ name: 'Error', message: 'boom' })).toBeNull()
  expect(retryAfterSeconds(undefined)).toBeNull()
})
