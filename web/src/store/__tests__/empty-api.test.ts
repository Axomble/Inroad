import { configureStore } from '@reduxjs/toolkit'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { retryAfterSeconds } from '@/lib/rtk-error'
import { emptyApi } from '../empty-api'
import authReducer, { clearSession } from '../slices/auth'

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
    // A real unauthenticated auth endpoint, to prove a 401 from one of these
    // (a wrong password) is not treated as an expired session.
    login: build.mutation<unknown, void>({ query: () => ({ url: '/auth/login', method: 'POST' }) }),
  }),
  overrideExisting: true,
})

function jsonResponse(body: unknown, status: number, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json', ...headers },
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
    const refreshRequest = fetchMock.mock.calls[1]![0] as Request
    expect(refreshRequest.url).toContain('/auth/refresh')
    expect(refreshRequest.method).toBe('POST')

    expect(result.data).toEqual({ ok: true })
    const state = store.getState()
    expect(state.auth.status).toBe('authed')
    expect(state.auth.accessToken).toBe('new-access-token')
  })

  test('a 401 from an unauthenticated auth endpoint does not attempt a refresh', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ error: 'invalid credentials' }, 401))
    vi.stubGlobal('fetch', fetchMock)

    const store = makeStore()
    const result = await store.dispatch(testApi.endpoints.login.initiate())

    // Exactly the login POST: a wrong password is not an expired session, so
    // there is nothing to refresh (this fired a doomed /auth/refresh on every
    // failed login attempt, which is what users saw on the login screen).
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect((fetchMock.mock.calls[0]![0] as Request).url).toContain('/auth/login')
    expect('error' in result && result.error).toBeTruthy()
    // Untouched: no session existed to clear, so the bootstrap's `idle` stands.
    expect(store.getState().auth.status).toBe('idle')
  })

  test('a 401 does not attempt a refresh once the session is known to be gone', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ error: 'unauthorized' }, 401))
    vi.stubGlobal('fetch', fetchMock)

    const store = makeStore()
    // `anon` is what a failed bootstrap or a failed refresh leaves behind.
    store.dispatch(clearSession())
    await store.dispatch(testApi.endpoints.ping.initiate())

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls.some((call) => (call[0] as Request).url.includes('/auth/refresh'))).toBe(false)
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

  // These drive a real request through the base query and then read the error
  // exactly as a component does — off the dispatch result, with no `meta` in
  // sight. That is the whole point: the header only reaches the UI because the
  // base query copies it onto the payload.
  test('a Retry-After delay in seconds is readable from the error a caller receives', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(jsonResponse({ error: 'rate_limited' }, 429, { 'retry-after': '120' })),
    )

    const result = await makeStore().dispatch(testApi.endpoints.ping.initiate())

    expect(retryAfterSeconds(result.error)).toBe(120)
  })

  test('an HTTP-date Retry-After is converted to a delay in seconds', async () => {
    const in90s = new Date(Date.now() + 90_000).toUTCString()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({}, 429, { 'retry-after': in90s })))

    const result = await makeStore().dispatch(testApi.endpoints.ping.initiate())

    const seconds = retryAfterSeconds(result.error)
    // Slack for the clock ticking between building the header and reading it.
    expect(seconds).toBeGreaterThanOrEqual(88)
    expect(seconds).toBeLessThanOrEqual(90)
  })

  test('a 429 without the header leaves the error otherwise untouched', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ error: 'rate_limited' }, 429)))

    const result = await makeStore().dispatch(testApi.endpoints.ping.initiate())

    expect(retryAfterSeconds(result.error)).toBeNull()
    expect(result.error).toMatchObject({ status: 429, data: { error: 'rate_limited' } })
  })

  test('a transport failure, which has no response to read a header from, still surfaces', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))

    const result = await makeStore().dispatch(testApi.endpoints.ping.initiate())

    expect(result.error).toMatchObject({ status: 'FETCH_ERROR' })
    expect(retryAfterSeconds(result.error)).toBeNull()
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
