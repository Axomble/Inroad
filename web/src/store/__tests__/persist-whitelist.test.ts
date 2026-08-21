import { beforeEach, expect, test } from 'vitest'
import { store, persistor } from '../index'
import { PERSIST_STORAGE_KEY } from '../persist-key'
import { readPersistedTheme } from '@/lib/theme'
import { setSession } from '../slices/auth'
import { setTheme, type ThemePreference } from '../slices/ui'
import { pushToast } from '../slices/toast'

// Security regression guard. The access token lives in memory ONLY — the persist
// whitelist must be ['ui'] and never include 'auth' or the RTK Query 'api' cache.
// A one-line whitelist slip would flush the Bearer token to localStorage, where
// any XSS could read it. This asserts against what redux-persist actually writes,
// so it fails loudly the moment anything else leaks into the persisted blob.

const session = {
  access_token: 'super-secret-access-token',
  expires_in: 900,
  user_id: 'user-1',
  active_workspace_id: 'workspace-1',
  role: 'owner',
  memberships: [],
}

beforeEach(() => {
  localStorage.clear()
})

test('redux-persist writes the ui slice but never the auth slice or its token', async () => {
  // Put a real token in the auth slice and mutate ui so both have persistable
  // content, then force redux-persist to flush its pending write.
  store.dispatch(setSession(session))
  store.dispatch(setTheme('dark'))
  await persistor.flush()

  const raw = localStorage.getItem(PERSIST_STORAGE_KEY)
  expect(raw).not.toBeNull()

  const persisted = JSON.parse(raw as string) as Record<string, unknown>
  expect(Object.keys(persisted)).toContain('ui')
  expect(Object.keys(persisted)).not.toContain('auth')

  // Belt-and-suspenders: the token string must not appear anywhere in storage.
  expect(raw).not.toContain('super-secret-access-token')
})

test('the RTK Query cache is never persisted', async () => {
  store.dispatch(setTheme('light'))
  await persistor.flush()

  const raw = localStorage.getItem(PERSIST_STORAGE_KEY)
  const persisted = JSON.parse(raw as string) as Record<string, unknown>
  // Server responses can carry contact and mailbox data; none of it belongs in
  // localStorage, so the api reducer must stay outside the whitelist.
  expect(Object.keys(persisted)).not.toContain('api')
})

// Toasts are transient by definition: one rehydrated from localStorage would
// announce an import that finished before the reload. Keeping them in their own
// reducer rather than inside `ui` is what makes that structural — this asserts
// the structure held.
test('toasts are never persisted', async () => {
  store.dispatch(pushToast({ tone: 'ok', text: 'Import finished.' }))
  // A toast changes no persisted subtree, so on its own it triggers no write
  // and this would assert against a stale blob. Force one with a ui change —
  // dark-then-light, so the pair is a real change from either starting value
  // AND leaves the theme where the next test's `setTheme('dark')` is also one.
  // (These tests share the module-singleton store; see persistThenRead below.)
  store.dispatch(setTheme('dark'))
  store.dispatch(setTheme('light'))
  await persistor.flush()

  const raw = localStorage.getItem(PERSIST_STORAGE_KEY) as string
  expect(Object.keys(JSON.parse(raw) as Record<string, unknown>)).not.toContain('toast')
  expect(raw).not.toContain('Import finished.')
})

test('the persisted ui slice round-trips the theme preference', async () => {
  store.dispatch(setTheme('dark'))
  await persistor.flush()

  const persisted = JSON.parse(localStorage.getItem(PERSIST_STORAGE_KEY) as string) as { ui: string }
  expect(JSON.parse(persisted.ui)).toMatchObject({ theme: 'dark' })
})

// The pre-paint read in lib/theme.ts parses redux-persist's blob by hand rather
// than through the library, so it has to be verified against a blob the real
// persistor wrote — not a hand-built fixture. This is what catches a storage
// shape change on a redux-persist upgrade.
/**
 * Persists `to` and reads it back through the pre-paint parser.
 *
 * Dispatches `from` first so `to` is always a real state change: redux-persist
 * skips the write when the persisted subtree is unchanged, and these tests share
 * the module-singleton store, so setting a value it already holds after
 * `localStorage.clear()` would assert against an empty blob.
 */
async function persistThenRead(from: ThemePreference, to: ThemePreference) {
  store.dispatch(setTheme(from))
  store.dispatch(setTheme(to))
  await persistor.flush()
  return readPersistedTheme()
}

test('readPersistedTheme parses what the real persistor writes', async () => {
  expect(await persistThenRead('light', 'dark')).toBe('dark')
  expect(await persistThenRead('dark', 'light')).toBe('light')
})

test('the in-memory store still holds the token it refused to persist', () => {
  store.dispatch(setSession(session))
  expect(store.getState().auth.accessToken).toBe('super-secret-access-token')
})
