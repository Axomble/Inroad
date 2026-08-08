import { screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { GoogleCallbackPage } from './google-callback-page'

// The landing route for the backend's Google callback. It holds the user for one
// request and then sends them on, so every test here is about where they end up —
// and about the fact that no access token ever rides in the URL: the session comes
// from exchanging the refresh cookie the callback already set.

const navigate = vi.fn()
let search: { signin?: string; return_to?: string } = {}
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  getRouteApi: () => ({ useSearch: () => search }),
  Link: ({ to, children }: { to: string; children: React.ReactNode }) => <a href={to}>{children}</a>,
}))

const jsonHeaders = { 'content-type': 'application/json' }
const SESSION = {
  access_token: 'tok-abc',
  expires_in: 900,
  user_id: 'u-1',
  active_workspace_id: 'w-1',
  role: 'owner',
  email: 'ops@acme.com',
  onboarding_completed_at: null,
  memberships: [
    { workspace_id: 'w-1', workspace_name: 'Acme', role: 'owner', onboarding_completed_at: null },
  ],
}

let refreshResponder: () => Response
let requests: string[]

beforeEach(() => {
  navigate.mockClear()
  search = {}
  requests = []
  refreshResponder = () => new Response(JSON.stringify(SESSION), { status: 200, headers: jsonHeaders })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      requests.push(url)
      return refreshResponder()
    }),
  )
})

afterEach(() => {
  Object.defineProperty(window, 'location', { configurable: true, value: ORIGINAL_LOCATION })
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const ORIGINAL_LOCATION = window.location

/** Swap in a stub `window.location` (keeping the real origin) to capture `assign`. */
function stubAssign() {
  const assign = vi.fn()
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...ORIGINAL_LOCATION, origin: ORIGINAL_LOCATION.origin, assign },
  })
  return assign
}

test('a successful sign-in exchanges the cookie for a session and enters the app', async () => {
  search = { signin: 'ok' }
  const { store } = renderWithProviders(<GoogleCallbackPage />)

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app', replace: true }))

  // The session came from /auth/refresh, not from anything in the URL.
  expect(requests.some((url) => url.includes('/auth/refresh'))).toBe(true)
  const state = store.getState() as { auth: { accessToken: string | null; status: string } }
  expect(state.auth.accessToken).toBe('tok-abc')
  expect(state.auth.status).toBe('authed')
})

test('it says what it is doing while the exchange is in flight', () => {
  search = { signin: 'ok' }
  renderWithProviders(<GoogleCallbackPage />)
  expect(screen.getByRole('status')).toHaveTextContent(/finishing sign-in/i)
})

// Only successes land here — the backend sends failures to /?google_error=. So an
// arrival WITHOUT signin=ok means something went wrong upstream, and it must reach
// the surface that reports it rather than sit on a blank page.
test('landing here without a success flag is forwarded to the login screen', async () => {
  renderWithProviders(<GoogleCallbackPage />)

  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: '/',
      search: { google_error: 'server_error' },
      replace: true,
    }),
  )
  // No session exchange is attempted when the sign-in didn't succeed.
  expect(requests.some((url) => url.includes('/auth/refresh'))).toBe(false)
})

test('a requested return_to is resumed with a full navigation, not the SPA router', async () => {
  search = { signin: 'ok', return_to: '/oauth2/authorize?client_id=abc&state=xyz' }
  const assign = stubAssign()

  renderWithProviders(<GoogleCallbackPage />)

  // A full navigation because the target may be the API's /oauth2/authorize, which
  // is not an SPA route.
  await waitFor(() => expect(assign).toHaveBeenCalledWith('/oauth2/authorize?client_id=abc&state=xyz'))
  expect(navigate).not.toHaveBeenCalledWith({ to: '/app', replace: true })
})

// The server validated this on the way out, but it arrives back as a URL param, so
// it is validated again before the browser is handed it.
test('an off-origin return_to is ignored and the user lands in the app', async () => {
  search = { signin: 'ok', return_to: 'https://evil.com' }
  const assign = stubAssign()

  renderWithProviders(<GoogleCallbackPage />)

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app', replace: true }))
  expect(assign).not.toHaveBeenCalled()
})

test('a failed cookie exchange still lands the user somewhere with an explanation', async () => {
  search = { signin: 'ok' }
  refreshResponder = () =>
    new Response(JSON.stringify({ error: 'no refresh cookie' }), { status: 401, headers: jsonHeaders })
  renderWithProviders(<GoogleCallbackPage />)

  await waitFor(() =>
    expect(navigate).toHaveBeenCalledWith({
      to: '/',
      search: { google_error: 'exchange_failed' },
      replace: true,
    }),
  )
})

test('the exchange runs once — a repeated one would race the refresh-token rotation', async () => {
  search = { signin: 'ok' }
  const { rerender } = renderWithProviders(<GoogleCallbackPage />)

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app', replace: true }))
  rerender(<GoogleCallbackPage />)

  await waitFor(() =>
    expect(requests.filter((url) => url.includes('/auth/refresh'))).toHaveLength(1),
  )
})
