import { expect, test } from 'vitest'
import { agentErrorMessage, approvalDecisionMessage } from '../error-copy'

const fallback = 'Message could not be sent. Your draft has been kept.'

// The delay reaches this function as a `retryAfter` field on the error, folded
// on by the shared base query (store/empty-api.ts) — `meta`, where the header
// actually arrives, never reaches a component.
test('a rate limit names the delay the server asked for, and stays vague without one', () => {
  expect(agentErrorMessage({ status: 429, data: undefined, retryAfter: 45 }, fallback)).toBe(
    'Too many requests. Try again in 45 seconds.',
  )
  expect(agentErrorMessage({ status: 429, data: undefined, retryAfter: 1 }, fallback)).toBe(
    'Too many requests. Try again in 1 second.',
  )
  expect(agentErrorMessage({ status: 429, data: undefined }, fallback)).toBe(
    'Too many requests. Try again in a moment.',
  )
})

test('a transport failure is never reported as an HTTP refusal', () => {
  expect(agentErrorMessage({ status: 'FETCH_ERROR', error: 'network down' }, fallback)).toBe(
    'Could not reach the server. Check your connection and try again.',
  )
  expect(agentErrorMessage(new Error('boom'), fallback)).toBe(
    'Could not reach the server. Check your connection and try again.',
  )
})

test('a rate-limited approval decision inherits the delay copy rather than its own generic line', () => {
  expect(approvalDecisionMessage({ status: 429, data: undefined, retryAfter: 10 })).toBe(
    'Too many requests. Try again in 10 seconds.',
  )
})

test('an already-decided approval is distinguished from one that is simply gone', () => {
  expect(approvalDecisionMessage({ status: 409, data: undefined })).toMatch(/already decided/i)
  expect(approvalDecisionMessage({ status: 404, data: undefined })).toMatch(/no longer exists/i)
})
