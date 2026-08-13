import { describe, expect, test } from 'vitest'
import { warmupErrorMessage } from './error-copy'

const fallback = 'The warmup history could not be loaded.'

describe('warmup error copy', () => {
  // A transport failure has no numeric status, so anything reading `error.status`
  // directly would report it as an HTTP refusal. It is narrowed through
  // `@/lib/rtk-error` instead.
  test('an unreachable server is not reported as an HTTP refusal', () => {
    const message = warmupErrorMessage({ status: 'FETCH_ERROR', error: 'network down' }, fallback)
    expect(message.toLowerCase()).toContain('reach')
    expect(message).not.toContain('undefined')
  })

  test('a 404 says the mailbox is not a participant in this workspace', () => {
    expect(warmupErrorMessage({ status: 404, data: {} }, fallback).toLowerCase()).toContain('participant')
  })

  test('a 403 names the access problem rather than the data', () => {
    expect(warmupErrorMessage({ status: 403, data: {} }, fallback).toLowerCase()).toContain('access')
  })

  test('an expired session says to refresh', () => {
    expect(warmupErrorMessage({ status: 401, data: {} }, fallback).toLowerCase()).toContain('session')
  })

  // The dangerous misreading of a failed history request is "nothing has
  // happened to this mailbox". Every message has to read as a failed request.
  test('a server failure never reads as an empty history', () => {
    for (const status of [500, 502, 503]) {
      const message = warmupErrorMessage({ status, data: {} }, fallback).toLowerCase()
      expect(message).toContain('server')
      expect(message).not.toMatch(/no (changes|history|transitions)/)
    }
  })

  test('an unmapped status falls back to the caller sentence', () => {
    expect(warmupErrorMessage({ status: 418, data: {} }, fallback)).toBe(fallback)
  })

  // The API answers `{"error": "…"}`; prose from the server beats generic copy.
  test('the server sentence is preferred when it sent prose', () => {
    const message = warmupErrorMessage({ status: 418, data: { error: 'the pool is being rebuilt' } }, fallback)
    expect(message).toBe('the pool is being rebuilt')
  })
})
