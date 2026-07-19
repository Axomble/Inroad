import { configureStore } from '@reduxjs/toolkit'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { emptyApi } from './empty-api'
import authReducer from './slices/auth'

// A throwaway endpoint injected purely to exercise baseQueryWithReauth —
// the generated api.ts endpoints aren't needed for this.
const testApi = emptyApi.injectEndpoints({
  endpoints: (build) => ({
    ping: build.query<{ ok: boolean }, void>({ query: () => ({ url: '/ping' }) }),
    // Distinct cache key per arg, so two concurrent calls are two genuinely
    // separate underlying requests instead of being deduped by RTK Query's
    // own subscription cache (which would otherwise collapse two identical
    // `ping.initiate()` calls into a single fetch before reauth ever sees them).
    pingAs: build.query<{ ok: boolean }, string>({ query: (who) => ({ url: `/ping/${who}` }) }),
  }),
  overrideExisting: true,
})

function jsonResponse(body: unknown, status: number) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

function makeStore() {
  return configureStore({
    reducer: { [emptyApi.reducerPath]: emptyApi.reducer, auth: authReducer },
    middleware: (getDefault) => getDefault().concat(emptyApi.middleware),
  })
}

const session = {
  access_token: 'new-access-token',
  expires_in: 900,
  user_id: 'user-1',
  active_workspace_id: 'workspace-1',
  role: 'owner',
  memberships: [],
}

describe('baseQueryWithReauth', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  test('a 401 triggers exactly one refresh, then retries the original request', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ message: 'unauthorized' }, 401)) // original /ping
      .mockResolvedValueOnce(jsonResponse(session, 200)) // /auth/refresh
      .mockResolvedValueOnce(jsonResponse({ ok: true }, 200)) // retried /ping
    vi.stubGlobal('fetch', fetchMock)

    const store = makeStore()
    const result = await store.dispatch(testApi.endpoints.ping.initiate())

    expect(fetchMock).toHaveBeenCalledTimes(3)
    const refreshRequest = fetchMock.mock.calls[1][0] as Request
    expect(refreshRequest.url).toContain('/auth/refresh')
    expect(refreshRequest.method).toBe('POST')

    expect(result.data).toEqual({ ok: true })
    const state = store.getState()
    expect(state.auth.status).toBe('authed')
    expect(state.auth.accessToken).toBe('new-access-token')
  })

  test('concurrent 401s share a single refresh call (single-flight)', async () => {
    const fetchMock = vi.fn().mockImplementation((request: Request) => {
      if (request.url.includes('/auth/refresh')) return Promise.resolve(jsonResponse(session, 200))
      if (request.url.includes('/ping/')) {
        // Each distinct ping fails 401 until the token has been refreshed.
        const authed = request.headers.get('authorization') === 'Bearer new-access-token'
        return Promise.resolve(jsonResponse({ ok: authed }, authed ? 200 : 401))
      }
      throw new Error(`unexpected request: ${request.url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const store = makeStore()
    const [a, b] = await Promise.all([
      store.dispatch(testApi.endpoints.pingAs.initiate('a')),
      store.dispatch(testApi.endpoints.pingAs.initiate('b')),
    ])

    expect(a.data).toEqual({ ok: true })
    expect(b.data).toEqual({ ok: true })
    const refreshCalls = fetchMock.mock.calls.filter((call) => (call[0] as Request).url.includes('/auth/refresh'))
    expect(refreshCalls).toHaveLength(1)
  })

  test('a failed refresh dispatches clearSession', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({}, 401)) // original /ping
      .mockResolvedValueOnce(jsonResponse({ message: 'invalid refresh token' }, 401)) // /auth/refresh fails too
    vi.stubGlobal('fetch', fetchMock)

    const store = makeStore()
    const result = await store.dispatch(testApi.endpoints.ping.initiate())

    expect(result.error).toBeDefined()
    const state = store.getState()
    expect(state.auth.status).toBe('anon')
    expect(state.auth.accessToken).toBeNull()
  })
})
