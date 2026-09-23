import { expect, test } from 'vitest'
import type { AuditEvent } from '@/store/api'
import { actorCopy, auditLogErrorMessage, metadataEntries, targetCopy } from '../audit-log-copy'

function event(overrides: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 'e-1',
    action: 'auth.login',
    actor_type: 'user',
    actor_id: 'u-1',
    actor_user_id: 'u-1',
    actor_email: 'jo@acme.test',
    target_type: null,
    target_id: null,
    ip: '203.0.113.7',
    user_agent: null,
    metadata: {},
    created_at: '2026-09-22T10:00:00Z',
    ...overrides,
  }
}

test('a signed-in user is named by email, and a removed one says so', () => {
  expect(actorCopy(event())).toEqual({ primary: 'jo@acme.test' })
  expect(actorCopy(event({ actor_email: null }))).toEqual({ primary: 'Deleted user' })
})

// THE distinction: an attempt nobody authenticated must never be attributed to
// the account whose email was typed.
test('a user actor with no id is an unknown failed sign-in, with the typed email shown as tried', () => {
  const copy = actorCopy(
    event({
      action: 'auth.login_failed',
      actor_id: null,
      actor_user_id: null,
      actor_email: null,
      metadata: { email: 'ceo@acme.test' },
    }),
  )
  expect(copy).toEqual({ primary: 'Unknown (failed sign-in)', secondary: 'tried ceo@acme.test' })
  expect(actorCopy(event({ actor_id: null, actor_email: null })).secondary).toBeUndefined()
})

test('delegated actors name the credential and whose authority it carried', () => {
  expect(actorCopy(event({ actor_type: 'api_key', actor_id: 'key-9' }))).toEqual({
    primary: 'API key key-9',
    secondary: 'created by jo@acme.test',
  })
  expect(actorCopy(event({ actor_type: 'oauth_client', actor_id: 'app-2' }))).toEqual({
    primary: 'Connected app app-2',
    secondary: 'for jo@acme.test',
  })
  expect(actorCopy(event({ actor_type: 'agent', actor_id: 'run-4' }))).toEqual({
    primary: 'Agent',
    secondary: 'for jo@acme.test',
  })
  expect(actorCopy(event({ actor_type: 'agent', actor_email: null })).secondary).toBeUndefined()
  expect(
    actorCopy(event({ actor_type: 'system', actor_id: 'warmup', actor_user_id: null, actor_email: null })),
  ).toEqual({ primary: 'System', secondary: 'warmup' })
})

test('targets join type and id, and a targetless event has none', () => {
  expect(targetCopy(event({ target_type: 'campaign', target_id: 'c-1' }))).toBe('campaign · c-1')
  expect(targetCopy(event())).toBeNull()
})

test('metadata reads in a stable order', () => {
  expect(metadataEntries({ to_role: 'admin', from_role: 'member' })).toEqual([
    ['from_role', 'member'],
    ['to_role', 'admin'],
  ])
})

test('errors are explained by cause', () => {
  expect(auditLogErrorMessage({ status: 403, data: { error: 'forbidden' } })).toMatch(/owners and admins only/)
  expect(auditLogErrorMessage({ status: 401, data: null })).toMatch(/session expired/)
  expect(auditLogErrorMessage({ status: 400, data: { error: 'since must be an RFC3339 timestamp' } })).toBe(
    'The server refused these filters (since must be an RFC3339 timestamp). Clear them and try again.',
  )
  expect(auditLogErrorMessage({ status: 'FETCH_ERROR', error: 'offline' })).toMatch(/Couldn't reach the server/)
  expect(auditLogErrorMessage({ status: 500, data: { error: 'could not list audit events' } })).toBe(
    'could not list audit events',
  )
})
