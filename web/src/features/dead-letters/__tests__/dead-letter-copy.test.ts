import { expect, test } from 'vitest'
import {
  attemptsText,
  deadLetterActionMessage,
  deadLetterErrorMessage,
  lastErrorText,
  payloadText,
  statusCopy,
  STATUS_COPY,
} from '../dead-letter-copy'

/** An RTK Query error as the transport reports it. */
function httpError(status: number, message?: string) {
  return { status, data: message ? { message } : {} }
}

/* --------------------------------------------------------------- the statuses */

// Only an untriaged row may be acted on. Both terminal states are decisions that
// have already been made, and offering an action on one would invite a second.
test('only pending is actionable', () => {
  expect(STATUS_COPY.pending.actionable).toBe(true)
  expect(STATUS_COPY.replayed.actionable).toBe(false)
  expect(STATUS_COPY.discarded.actionable).toBe(false)
})

// Discarded is the one an operator can most easily misread as resolved.
test('discarded says the work did not happen', () => {
  expect(STATUS_COPY.discarded.detail).toMatch(/does not mean the work succeeded/i)
})

// The status vocabulary is closed in the contract but the JSON boundary is not, and
// an unknown state must never be treated as safe to act on.
test('an unrecognised status is shown as it arrived and is never actionable', () => {
  const unknown = statusCopy('quarantined')

  expect(unknown.label).toBe('quarantined')
  expect(unknown.actionable).toBe(false)
  expect(unknown.detail).toMatch(/no reading for the state/i)
})

/* ------------------------------------------------------------- action errors */

// The exactly-once claim doing its job. Reporting this as a failure would tell an
// operator to retry the one case where the system already guaranteed correctness.
test('a 409 reads as already handled, not as a failure to retry', () => {
  const message = deadLetterActionMessage(httpError(409), 'replay')

  expect(message).toMatch(/already/i)
  expect(message).toMatch(/nothing was run twice/i)
  expect(message).not.toMatch(/try again/i)
})

// The contract calls 422 permanent for this row: "do not retry".
test('a 422 says the row can never be replayed and points at discard', () => {
  const message = deadLetterActionMessage(httpError(422), 'replay')

  expect(message).toMatch(/permanent/i)
  expect(message).toMatch(/discard/i)
  expect(message).not.toMatch(/try again/i)
})

// Replay needs campaigns:send precisely because it delivers mail, so the refusal
// explains the permission by what the action does rather than naming a scope.
test('a 403 on replay explains that replaying sends mail', () => {
  expect(deadLetterActionMessage(httpError(403), 'replay')).toMatch(/re-sends mail/i)
  expect(deadLetterActionMessage(httpError(403), 'discard')).toMatch(/send permission/i)
})

// A transport failure carries a string status tag, not a number. Reporting it as an
// HTTP refusal would name a rejection the server never made.
test('an unreachable server says nothing changed', () => {
  const message = deadLetterActionMessage({ status: 'FETCH_ERROR', error: 'down' }, 'discard')

  expect(message).toMatch(/could not reach the server/i)
  expect(message).toMatch(/nothing changed/i)
})

/* --------------------------------------------------------------- read errors */

// The danger unique to this screen: an empty list is the reassuring normal state, so
// a failed read must never be able to read as "nothing has failed".
test('an unreachable server says the failure state is unknown, not clean', () => {
  const message = deadLetterErrorMessage({ status: 'FETCH_ERROR', error: 'down' }, 'fallback')

  expect(message).toMatch(/unknown/i)
  expect(message).not.toMatch(/no task/i)
})

test('a 422 on the list blames the filter rather than the queue', () => {
  expect(deadLetterErrorMessage(httpError(422), 'fallback')).toMatch(/status filter/i)
})

test('an unmapped status falls back to the server detail, then the caller fallback', () => {
  expect(deadLetterErrorMessage(httpError(500, 'the queue is unavailable'), 'fallback')).toBe(
    'the queue is unavailable',
  )
  expect(deadLetterErrorMessage(httpError(500), 'fallback')).toBe('fallback')
})

// `serverDetail` drops single-token bodies precisely so machine codes do not reach
// an operator. Worth pinning here: this screen surfaces raw server text more than
// most, and "task_payload_invalid" rendered as advice would be unreadable.
test('a machine code is not passed through as operator copy', () => {
  expect(deadLetterErrorMessage(httpError(500, 'task_payload_invalid'), 'fallback')).toBe('fallback')
})

/* ------------------------------------------------------------------ the row */

// An empty last_error is allowed by the contract, and blank space would read as
// "it failed for no reason".
test('an empty last error is stated as an absence', () => {
  expect(lastErrorText('')).toMatch(/no error text was recorded/i)
  expect(lastErrorText('   ')).toMatch(/no error text was recorded/i)
  expect(lastErrorText('  smtp 550  ')).toBe('smtp 550')
})

// attempt_count INCLUDES the final failure, so wording it as retries would be off
// by one in the direction that understates how hard the system tried.
test('attempts are counted as attempts, singular at one', () => {
  expect(attemptsText(1)).toBe('1 attempt before giving up')
  expect(attemptsText(3)).toBe('3 attempts before giving up')
  expect(attemptsText(1)).not.toMatch(/retr/i)
})

test('a payload renders as readable JSON', () => {
  expect(payloadText({ workspace_id: 'ws-1', enrollment_id: 'e-1' })).toBe(
    '{\n  "workspace_id": "ws-1",\n  "enrollment_id": "e-1"\n}',
  )
})

