import { expect, test } from 'vitest'
import {
  DELIVERY_STATUS_COPY,
  deliveryAttemptsText,
  deliveryErrorMessage,
  eventTypeBadgeLabels,
  eventTypeLabel,
  eventTypesSummary,
  responseStatusText,
  webhookActionMessage,
  webhookErrorMessage,
} from '../webhook-copy'

/** An RTK Query error as the transport reports it. */
function httpError(status: number, message?: string) {
  return { status, data: message ? { message } : {} }
}

/* ------------------------------------------------------------ event types */

test('a known event type reads with its friendly label', () => {
  expect(eventTypeLabel('reply.received')).toBe('Reply received')
})

// The contract lets `event_types` on a read carry a type this build was shipped
// before — folding it into "other" would make it unfindable in the payload the
// receiver actually gets.
test('an unrecognised event type is shown as it arrived', () => {
  expect(eventTypeLabel('mailbox.disconnected')).toBe('mailbox.disconnected')
})

test('an empty subscription summarises as every event, not as nothing', () => {
  expect(eventTypesSummary([])).toBe('All events')
  expect(eventTypeBadgeLabels([])).toEqual(['All events'])
})

test('a subscription summarises as its labelled events', () => {
  expect(eventTypesSummary(['reply.received', 'email.bounced'])).toBe('Reply received, Email bounced')
  expect(eventTypeBadgeLabels(['reply.received'])).toEqual(['Reply received'])
})

/* -------------------------------------------------------------- the errors */

test('an unreachable server reads as unreachable, not as a generic failure, for the list', () => {
  expect(webhookErrorMessage(undefined, 'fallback')).toMatch(/could not reach the server/i)
})

test('a 401 on the list says the session expired', () => {
  expect(webhookErrorMessage(httpError(401), 'fallback')).toMatch(/session expired/i)
})

test('the list falls back to the caller-provided copy when the server sent no detail', () => {
  expect(webhookErrorMessage(httpError(500), 'fallback copy')).toBe('fallback copy')
})

test('the list prefers the server-sent detail over the fallback', () => {
  expect(webhookErrorMessage(httpError(500, 'workspace is over its endpoint limit'), 'fallback')).toBe(
    'workspace is over its endpoint limit',
  )
})

test('an unreachable server on an action names the action, not a status code', () => {
  const message = webhookActionMessage(undefined, 'create')
  expect(message).toMatch(/could not reach the server/i)
  expect(message).toMatch(/registering the endpoint/i)
})

test('a 404 on delete, rotate or ping says the endpoint is already gone', () => {
  for (const action of ['delete', 'rotate', 'ping'] as const) {
    expect(webhookActionMessage(httpError(404), action)).toMatch(/no longer here/i)
  }
})

// The one 422 this screen's create flow can hit: the SSRF guard or the event
// enum rejected the request. Never a bare status code.
test('a 422 on create explains the SSRF guard and the event-type enum', () => {
  const message = webhookActionMessage(httpError(422), 'create')
  expect(message).toMatch(/ssrf guard/i)
  expect(message).not.toMatch(/^422$/)
})

// THE case this test guards: a mapped, specific message — never the raw status
// code and never a context-free "Something went wrong".
test('an unmapped failure names the action rather than reading as a bare failure', () => {
  const message = webhookActionMessage(httpError(500), 'create')
  expect(message).not.toBe('422')
  expect(message).not.toBe('500')
  expect(message.toLowerCase()).not.toBe('something went wrong.')
  expect(message).toMatch(/registering the endpoint/i)
})

test('the server-sent detail wins over the generic action failure text', () => {
  expect(webhookActionMessage(httpError(500, 'endpoint limit reached for this workspace'), 'create')).toBe(
    'endpoint limit reached for this workspace',
  )
})

/* ----------------------------------------------------------- deliveries */

test('every delivery status has copy, keyed by the contract union', () => {
  expect(DELIVERY_STATUS_COPY.pending.label).toBe('Pending')
  expect(DELIVERY_STATUS_COPY.delivered.label).toBe('Delivered')
  expect(DELIVERY_STATUS_COPY.failed.label).toBe('Failed')
})

test('a null response status reads as no response, not as a blank', () => {
  expect(responseStatusText(null)).toBe('no response')
  expect(responseStatusText(200)).toBe('HTTP 200')
  expect(responseStatusText(503)).toBe('HTTP 503')
})

test('delivery attempts are pluralised correctly', () => {
  expect(deliveryAttemptsText(1)).toBe('1 attempt')
  expect(deliveryAttemptsText(3)).toBe('3 attempts')
})

test('a stale delivery cursor (400) reports the recovery, not a failure to retry', () => {
  expect(deliveryErrorMessage(httpError(400), 'fallback')).toMatch(/page link expired/i)
})

test('an unreachable server for the delivery log reads as unreachable', () => {
  expect(deliveryErrorMessage(undefined, 'fallback')).toMatch(/could not reach the server/i)
})
