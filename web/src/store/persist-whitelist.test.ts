import { beforeEach, expect, test } from 'vitest'
import { store, persistor } from './index'
import { setSession } from './slices/auth'
import { toggleSidebar } from './slices/ui'

// Security regression guard. The access token lives in memory ONLY — the persist
// whitelist must be ['ui'] and never include 'auth'. A one-line whitelist slip
// would flush the Bearer token to localStorage, where any XSS could read it.
// This test asserts against what redux-persist actually writes to storage, so it
// fails loudly the moment 'auth' (or the token) leaks into the persisted blob.

const PERSIST_KEY = 'persist:inroad'

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
  store.dispatch(toggleSidebar())
  await persistor.flush()

  const raw = localStorage.getItem(PERSIST_KEY)
  expect(raw).not.toBeNull()

  const persisted = JSON.parse(raw as string) as Record<string, unknown>
  expect(Object.keys(persisted)).toContain('ui')
  expect(Object.keys(persisted)).not.toContain('auth')

  // Belt-and-suspenders: the token string must not appear anywhere in storage.
  expect(raw).not.toContain('super-secret-access-token')
})

test('the in-memory store still holds the token it refused to persist', () => {
  store.dispatch(setSession(session))
  expect(store.getState().auth.accessToken).toBe('super-secret-access-token')
})
