import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { OAuthConsentPage } from './oauth-consent-page'

// The consent screen reads `consent_id` from the route search via getRouteApi and
// renders a <Link> back home; stub both. `searchParams` is set per-test.
let searchParams: { consent_id?: string } = {}
vi.mock('@tanstack/react-router', () => ({
  getRouteApi: () => ({ useSearch: () => searchParams }),
  Link: ({ to, children, ...props }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

const jsonHeaders = { 'content-type': 'application/json' }
const ORIGINAL_LOCATION = window.location

const CONSENT = {
  client_name: 'Acme CRM',
  requested_scopes: ['contacts:read', 'lists:write'],
  redirect_uri: 'https://app.acme.com/oauth/callback',
}

const authedState = {
  auth: {
    status: 'authed' as const,
    accessToken: 'tok',
    activeWorkspaceId: 'w1',
    memberships: [{ workspace_id: 'w1', workspace_name: 'Growth Team', role: 'admin' }],
  },
}

let dataResponder: () => Response
let decideResponder: () => Response
let lastDecision: string | null
let assignMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  searchParams = { consent_id: 'consent-1' }
  lastDecision = null
  dataResponder = () => new Response(JSON.stringify(CONSENT), { status: 200, headers: jsonHeaders })
  decideResponder = () =>
    new Response(JSON.stringify({ redirect_to: 'https://app.acme.com/oauth/callback?code=xyz&state=s' }), {
      status: 200,
      headers: jsonHeaders,
    })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const method = init?.method ?? request?.method ?? 'GET'
      if (url.includes('/oauth2/consent')) {
        if (method === 'POST') {
          const raw = request ? await request.text() : String(init?.body ?? '{}')
          lastDecision = (JSON.parse(raw) as { decision: string }).decision
          return decideResponder()
        }
        return dataResponder()
      }
      return new Response(null, { status: 404 })
    }),
  )

  // jsdom's location.assign throws on real navigation; swap a stub that keeps a
  // valid origin (the provider URL is built from it) and captures the hand-off.
  assignMock = vi.fn()
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { origin: ORIGINAL_LOCATION.origin, href: ORIGINAL_LOCATION.href, assign: assignMock },
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  Object.defineProperty(window, 'location', { configurable: true, value: ORIGINAL_LOCATION })
})

test('shows a loading state before the request resolves', () => {
  renderWithProviders(<OAuthConsentPage />, { preloadedState: authedState })
  expect(screen.getByText(/loading authorization request/i)).toBeInTheDocument()
})

test('missing consent_id renders a clear error instead of crashing', async () => {
  searchParams = {}
  renderWithProviders(<OAuthConsentPage />, { preloadedState: authedState })
  expect(await screen.findByText(/missing authorization request/i)).toBeInTheDocument()
})

test('renders the client, scope labels, and workspace on success', async () => {
  renderWithProviders(<OAuthConsentPage />, { preloadedState: authedState })

  expect(await screen.findByText('Acme CRM')).toBeInTheDocument()
  // Human-readable scope labels, not raw values.
  expect(screen.getByText('Read your contacts')).toBeInTheDocument()
  expect(screen.getByText('Create and edit your contact lists')).toBeInTheDocument()
  // The workspace the grant covers, and the redirect host.
  expect(screen.getByText('Growth Team')).toBeInTheDocument()
  expect(screen.getByText('app.acme.com')).toBeInTheDocument()
})

test('a 404 renders the invalid/expired screen', async () => {
  dataResponder = () => new Response(JSON.stringify({ error: 'not_found' }), { status: 404, headers: jsonHeaders })
  renderWithProviders(<OAuthConsentPage />, { preloadedState: authedState })
  expect(await screen.findByText(/invalid or has expired/i)).toBeInTheDocument()
})

test('Approve posts the decision and hands off to the backend redirect_to', async () => {
  renderWithProviders(<OAuthConsentPage />, { preloadedState: authedState })

  fireEvent.click(await screen.findByRole('button', { name: /^approve$/i }))

  await waitFor(() =>
    expect(assignMock).toHaveBeenCalledWith('https://app.acme.com/oauth/callback?code=xyz&state=s'),
  )
  expect(lastDecision).toBe('approve')
})

test('Deny posts a deny decision and follows the access_denied redirect_to', async () => {
  decideResponder = () =>
    new Response(JSON.stringify({ redirect_to: 'https://app.acme.com/oauth/callback?error=access_denied&state=s' }), {
      status: 200,
      headers: jsonHeaders,
    })

  renderWithProviders(<OAuthConsentPage />, { preloadedState: authedState })

  fireEvent.click(await screen.findByRole('button', { name: /^deny$/i }))

  await waitFor(() =>
    expect(assignMock).toHaveBeenCalledWith('https://app.acme.com/oauth/callback?error=access_denied&state=s'),
  )
  expect(lastDecision).toBe('deny')
})

test('a decision error surfaces without navigating or losing the consent context', async () => {
  decideResponder = () => new Response(JSON.stringify({ error: 'expired' }), { status: 404, headers: jsonHeaders })

  renderWithProviders(<OAuthConsentPage />, { preloadedState: authedState })

  fireEvent.click(await screen.findByRole('button', { name: /^approve$/i }))

  expect(await screen.findByText(/this authorization request has expired/i)).toBeInTheDocument()
  expect(assignMock).not.toHaveBeenCalled()
  // Still on the consent card.
  expect(screen.getByText('Acme CRM')).toBeInTheDocument()
})
