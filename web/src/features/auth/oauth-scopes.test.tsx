import { expect, test } from 'vitest'
import { API_KEY_SCOPES } from './api-key-scopes'
import { OAUTH_GRANTABLE_SCOPES, OAUTH_SCOPE_GROUPS, scopeConsentLabel } from './oauth-scopes'

test('every grantable scope has a distinct human-readable consent label', () => {
  for (const scope of OAUTH_GRANTABLE_SCOPES) {
    const label = scopeConsentLabel(scope)
    // A real sentence, never the raw scope value (the fallback).
    expect(label).not.toBe(scope)
    expect(label.length).toBeGreaterThan(0)
  }
  const labels = OAUTH_GRANTABLE_SCOPES.map(scopeConsentLabel)
  expect(new Set(labels).size).toBe(labels.length)
})

test('the grantable set excludes non-OAuth scopes (send) but keeps read/write', () => {
  expect(OAUTH_GRANTABLE_SCOPES).not.toContain('campaigns:send')
  expect(OAUTH_GRANTABLE_SCOPES).toContain('contacts:read')
  expect(OAUTH_GRANTABLE_SCOPES).toContain('lists:write')
  // Grantable scopes are a strict subset of the API-key scope vocabulary.
  for (const scope of OAUTH_GRANTABLE_SCOPES) {
    expect(API_KEY_SCOPES).toContain(scope)
  }
})

test('the scope picker groups only expose grantable scopes', () => {
  const grouped = OAUTH_SCOPE_GROUPS.flatMap((group) => group.scopes.map((s) => s.value))
  expect([...grouped].sort()).toEqual([...OAUTH_GRANTABLE_SCOPES].sort())
})

test('an unknown scope falls back to its raw value rather than being hidden', () => {
  expect(scopeConsentLabel('mystery:scope')).toBe('mystery:scope')
})
