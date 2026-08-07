import { expect, test } from 'vitest'
import { crmErrorMessage } from './error-copy'

// The CRM service answers destructive requests with prose the user can act on
// ("stage still has 3 deal(s)"), which is worth far more than a generic
// sentence — but only if it survives the trip to the screen.

const fallback = 'It could not be saved.'
const http = (status: number, data: unknown) => ({ status, data })

test('surfaces the reason a delete was refused', () => {
  expect(crmErrorMessage(http(409, { error: 'the default pipeline cannot be deleted' }), fallback)).toBe(
    'the default pipeline cannot be deleted',
  )
  expect(crmErrorMessage(http(409, { error: 'stage still has 3 deal(s); move them first' }), fallback)).toBe(
    'stage still has 3 deal(s); move them first',
  )
})

test('surfaces validation prose from a rejected write', () => {
  expect(crmErrorMessage(http(422, { error: 'currency must be a three-letter ISO code' }), fallback)).toBe(
    'currency must be a three-letter ISO code',
  )
})

test('falls back to its own words when the server sends only a machine code', () => {
  // A bare token like `not_found` reads as noise mid-sentence.
  expect(crmErrorMessage(http(409, { error: 'conflict' }), fallback)).toBe(
    'That conflicts with an existing record. Reload and try again.',
  )
})

test('a deleted record reads as gone, not as a conflict', () => {
  expect(crmErrorMessage(http(404, { error: 'CRM record not found' }), fallback)).toBe(
    'That CRM record no longer exists — it may have been deleted.',
  )
})

test('a missing scope names the permission the user lacks', () => {
  expect(crmErrorMessage(http(403, { error: 'forbidden' }), fallback)).toContain('crm:read')
})

test('a transport failure is never reported as an HTTP refusal', () => {
  expect(crmErrorMessage({ status: 'FETCH_ERROR', error: 'network down' }, fallback)).toBe(
    'Could not reach the server. Check your connection and try again.',
  )
  // A thrown JS error carries no status either.
  expect(crmErrorMessage(new Error('boom'), fallback)).toBe(
    'Could not reach the server. Check your connection and try again.',
  )
})

// The delay reaches this function as a `retryAfter` field on the error, folded
// on by the shared base query (store/empty-api.ts) — `meta` never gets this far.
test('a rate limit names the delay the server asked for, and stays vague without one', () => {
  expect(crmErrorMessage({ status: 429, data: undefined, retryAfter: 30 }, fallback)).toBe(
    'Too many requests. Try again in 30 seconds.',
  )
  expect(crmErrorMessage({ status: 429, data: undefined, retryAfter: 1 }, fallback)).toBe(
    'Too many requests. Try again in 1 second.',
  )
  expect(crmErrorMessage(http(429, undefined), fallback)).toBe('Too many requests. Try again in a moment.')
})

test('a server fault is generic, never the server’s internals', () => {
  expect(crmErrorMessage(http(500, { error: 'pq: deadlock detected on relation crm_deals' }), fallback)).toBe(
    'The server had a problem loading CRM data. Try again in a moment.',
  )
})
