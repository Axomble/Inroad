import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { ApiKeysPanel } from '../api-keys-panel'

// Radix AlertDialog touches pointer + scroll APIs jsdom doesn't implement.
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }
const admin = { auth: { role: 'admin', status: 'authed' as const, activeWorkspaceId: 'w1' } }

const FULL_TOKEN = 'inrd_abcd_supersecretvalue1234567890'

type ApiKey = {
  id: string
  name: string
  prefix: string
  scopes: string[]
  created_at: string
  last_used_at: string | null
  expires_at: string | null
  revoked_at: string | null
}

let keys: ApiKey[]
let lastCreateBody: { name: string; scopes: string[]; expires_at: string | null } | null

beforeEach(() => {
  keys = []
  lastCreateBody = null

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const method = init?.method ?? request?.method ?? 'GET'

      if (url.includes('/auth/api-keys')) {
        if (method === 'POST') {
          // fetchBaseQuery calls fetch(Request), so the body rides on the
          // Request object, not the init arg.
          const raw = request ? await request.text() : String(init?.body ?? '{}')
          const body = JSON.parse(raw) as { name: string; scopes: string[]; expires_at: string | null }
          lastCreateBody = body
          const created: ApiKey = {
            id: 'k1',
            name: body.name,
            prefix: 'abcd',
            scopes: body.scopes,
            created_at: new Date().toISOString(),
            last_used_at: null,
            expires_at: null,
            revoked_at: null,
          }
          keys = [created]
          return new Response(JSON.stringify({ token: FULL_TOKEN, api_key: created }), {
            status: 201,
            headers: jsonHeaders,
          })
        }
        if (method === 'DELETE') {
          keys = keys.map((k) => ({ ...k, revoked_at: new Date().toISOString() }))
          return new Response(null, { status: 204 })
        }
        return new Response(JSON.stringify({ api_keys: keys }), { status: 200, headers: jsonHeaders })
      }
      return new Response('not found', { status: 404 })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('non-admins get an admins-only state and no create action', async () => {
  renderWithProviders(<ApiKeysPanel />, {
    preloadedState: { auth: { role: 'member', status: 'authed', activeWorkspaceId: 'w1' } },
  })

  expect(await screen.findByText('Admins only')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /create api key/i })).not.toBeInTheDocument()
})

test('shows the empty state for an admin with no keys', async () => {
  renderWithProviders(<ApiKeysPanel />, { preloadedState: admin })
  expect(await screen.findByText('No API keys yet')).toBeInTheDocument()
})

test('create flow reveals the full token exactly once and clears it on close', async () => {
  renderWithProviders(<ApiKeysPanel />, { preloadedState: admin })

  // Wait for the list to settle — the create action is disabled while loading.
  await screen.findByText('No API keys yet')
  fireEvent.click(screen.getByRole('button', { name: /create api key/i }))

  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'CI deploy bot' } })
  // Grant at least one scope (create is blocked otherwise).
  const firstScope = screen.getAllByRole('checkbox')[0]
  expect(firstScope).toBeDefined()
  fireEvent.click(firstScope as HTMLElement)

  fireEvent.click(screen.getByRole('button', { name: /create key/i }))

  // The full token is shown once, behind the one-time warning.
  expect(await screen.findByText(FULL_TOKEN)).toBeInTheDocument()
  expect(screen.getByText(/only time the full token is shown/i)).toBeInTheDocument()

  // Dismiss — the token must be gone from the DOM (never re-derivable).
  fireEvent.click(screen.getByRole('button', { name: /^done$/i }))
  await waitFor(() => expect(screen.queryByText(FULL_TOKEN)).not.toBeInTheDocument())

  // The list refetched and now shows the created key (metadata only, no token).
  expect(await screen.findByText('CI deploy bot')).toBeInTheDocument()
})

test('create is disabled until a name and at least one scope are provided', async () => {
  renderWithProviders(<ApiKeysPanel />, { preloadedState: admin })

  await screen.findByText('No API keys yet')
  fireEvent.click(screen.getByRole('button', { name: /create api key/i }))
  const create = screen.getByRole('button', { name: /create key/i })
  expect(create).toBeDisabled()

  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Key' } })
  expect(create).toBeDisabled() // still no scope

  const firstScope = screen.getAllByRole('checkbox')[0]
  fireEvent.click(firstScope as HTMLElement)
  expect(create).toBeEnabled()
})

test('a chosen expiry date is sent as end-of-day in the local zone, not UTC midnight', async () => {
  renderWithProviders(<ApiKeysPanel />, { preloadedState: admin })

  await screen.findByText('No API keys yet')
  fireEvent.click(screen.getByRole('button', { name: /create api key/i }))

  fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Expiring key' } })
  const firstScope = screen.getAllByRole('checkbox')[0]
  fireEvent.click(firstScope as HTMLElement)
  fireEvent.change(screen.getByLabelText(/expires/i), { target: { value: '2026-08-01' } })

  fireEvent.click(screen.getByRole('button', { name: /create key/i }))
  await screen.findByText(FULL_TOKEN)

  expect(lastCreateBody?.expires_at).not.toBeNull()
  const sent = new Date(lastCreateBody?.expires_at as string)
  // The instant must fall on the LOCAL calendar day the user picked — a UTC
  // midnight bug would land on 2026-08-01T00:00:00Z, which is still Aug 1 in
  // UTC but reads as the wrong day / an early expiry west of UTC.
  expect(sent.getFullYear()).toBe(2026)
  expect(sent.getMonth()).toBe(7) // August (0-indexed)
  expect(sent.getDate()).toBe(1)
  expect(sent.getHours()).toBe(23)
  expect(sent.getMinutes()).toBe(59)
})

test('revoking a key confirms and surfaces the outcome', async () => {
  keys = [
    {
      id: 'k1',
      name: 'Legacy key',
      prefix: 'abcd',
      scopes: ['mailboxes:read'],
      created_at: new Date().toISOString(),
      last_used_at: null,
      expires_at: null,
      revoked_at: null,
    },
  ]

  renderWithProviders(<ApiKeysPanel />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /revoke api key legacy key/i }))
  fireEvent.click(await screen.findByRole('button', { name: /^revoke key$/i }))

  await waitFor(() =>
    expect(screen.getByRole('status')).toHaveTextContent(/api key “legacy key” was revoked/i),
  )
})
