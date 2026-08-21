import { expect, test } from 'vitest'
import { recordErrorMessage } from '../error-copy'

// Notes, tasks and activity attach to contacts, companies and deals alike, so this
// copy has to stay true whichever record type failed. Naming a domain or a scope
// here would send a reader after the wrong permission on two records out of three.

const fallback = 'It could not be saved.'
const http = (status: number, data: unknown) => ({ status, data })

test('names no domain and no scope, whichever record type failed', () => {
  expect(recordErrorMessage(http(500, { error: 'boom' }), fallback)).toBe(
    'The server had a problem. Try again in a moment.',
  )
  expect(recordErrorMessage(http(404, {}), fallback)).toBe('That record no longer exists — it may have been deleted.')
  expect(recordErrorMessage(http(403, { error: 'forbidden' }), fallback)).toBe('You do not have permission to do that.')

  for (const status of [403, 404, 500]) {
    expect(recordErrorMessage(http(status, { error: 'x' }), fallback)).not.toMatch(/crm|CRM|contacts:/)
  }
})

test('surfaces the server’s own prose when it sent something actionable', () => {
  expect(recordErrorMessage(http(422, { error: 'body must not be empty' }), fallback)).toBe('body must not be empty')
  expect(recordErrorMessage(http(409, { error: 'that note was already deleted' }), fallback)).toBe(
    'that note was already deleted',
  )
})

test('falls back to its own words when the server sends only a machine code', () => {
  // A bare token like `conflict` reads as noise mid-sentence.
  expect(recordErrorMessage(http(409, { error: 'conflict' }), fallback)).toBe(
    'That conflicts with an existing record. Reload and try again.',
  )
})

test('a transport failure is never reported as an HTTP refusal', () => {
  expect(recordErrorMessage({ status: 'FETCH_ERROR', error: 'network down' }, fallback)).toBe(
    'Could not reach the server. Check your connection and try again.',
  )
  expect(recordErrorMessage(new Error('boom'), fallback)).toBe(
    'Could not reach the server. Check your connection and try again.',
  )
})

test('a rate limit names the delay the server asked for', () => {
  expect(recordErrorMessage({ status: 429, data: undefined, retryAfter: 30 }, fallback)).toBe(
    'Too many requests. Try again in 30 seconds.',
  )
  expect(recordErrorMessage(http(429, undefined), fallback)).toBe('Too many requests. Try again in a moment.')
})
