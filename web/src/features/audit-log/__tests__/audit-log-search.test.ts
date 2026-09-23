import { expect, test } from 'vitest'
import { auditFilterArgs, hasFilters, isRangeInverted, parseAuditLogSearch } from '../audit-log-search'
import { ACTION_GROUPS, actionLabel } from '../audit-vocabulary'

test('keeps a whole action, a category prefix, a known actor type and real days', () => {
  expect(parseAuditLogSearch({ action: 'campaign', actor: 'api_key', from: '2026-09-01', to: '2026-09-03' })).toEqual({
    action: 'campaign',
    actor: 'api_key',
    from: '2026-09-01',
    to: '2026-09-03',
  })
  expect(parseAuditLogSearch({ action: 'auth.login_failed' }).action).toBe('auth.login_failed')
})

// A hand-edited link must open the unfiltered log rather than a 400.
test('drops anything the server would refuse', () => {
  expect(
    parseAuditLogSearch({
      // Not a category: the API matches prefixes on a segment boundary only.
      action: 'campaigns',
      actor: 'superuser',
      // Passes the pattern, is not a day.
      from: '2026-02-30',
      to: 'yesterday',
    }),
  ).toEqual({ action: undefined, actor: undefined, from: undefined, to: undefined })
  // Prototype keys are not actions either.
  expect(parseAuditLogSearch({ action: 'constructor', actor: 'toString' })).toEqual({
    action: undefined,
    actor: undefined,
    from: undefined,
    to: undefined,
  })
})

test('turns days into local-midnight instants, with `to` inclusive of its whole day', () => {
  expect(auditFilterArgs({ from: '2026-09-01', to: '2026-09-03', actor: 'user', action: 'auth' })).toEqual({
    action: 'auth',
    actorType: 'user',
    since: new Date(2026, 8, 1).toISOString(),
    // The API's `until` is exclusive, so the 3rd ends at the 4th's midnight.
    until: new Date(2026, 8, 4).toISOString(),
  })
  // No filters, no arguments — not `action: undefined` keys the cache would split on.
  expect(auditFilterArgs({})).toEqual({})
})

test('a backwards range is inverted; a single day is not', () => {
  expect(isRangeInverted({ from: '2026-09-03', to: '2026-09-01' })).toBe(true)
  expect(isRangeInverted({ from: '2026-09-03', to: '2026-09-03' })).toBe(false)
  expect(isRangeInverted({ from: '2026-09-03' })).toBe(false)
})

test('hasFilters is false only for the unfiltered log', () => {
  expect(hasFilters({})).toBe(false)
  expect(hasFilters({ to: '2026-09-03' })).toBe(true)
})

test('every action is grouped under its own category, and labels fall back to the raw name', () => {
  for (const group of ACTION_GROUPS) {
    expect(group.actions.length).toBeGreaterThan(0)
    for (const { action } of group.actions) expect(action.startsWith(`${group.category}.`)).toBe(true)
  }
  expect(actionLabel('member.role_changed')).toBe('Role changed')
  // A newer server's action shows as itself rather than as a blank.
  expect(actionLabel('billing.plan_changed')).toBe('billing.plan_changed')
})
