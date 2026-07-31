import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { ConnectedAppsPanel } from './connected-apps-panel'

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

type Client = {
  client_id: string
  client_secret?: string
  client_name: string
  redirect_uris: string[]
  grant_types: string[]
  response_types: string[]
  scope: string
  client_type: 'public' | 'confidential'
  token_endpoint_auth_method: string
  created_at: string
  revoked_at?: string | null
}

let clients: Client[]
let lastRegisterBody: {
  client_name: string
  redirect_uris: string[]
  scope?: string
  token_endpoint_auth_method?: string
} | null
let registerResponder: (body: NonNullable<typeof lastRegisterBody>) => Response

function baseClient(body: NonNullable<typeof lastRegisterBody>, overrides: Partial<Client> = {}): Client {
  return {
    client_id: 'app_generated_1',
    client_name: body.client_name,
    redirect_uris: body.redirect_uris,
    grant_types: ['authorization_code'],
    response_types: ['code'],
    scope: body.scope ?? '',
    client_type: body.token_endpoint_auth_method === 'none' ? 'public' : 'confidential',
    token_endpoint_auth_method: body.token_endpoint_auth_method ?? 'none',
    created_at: new Date().toISOString(),
    revoked_at: null,
    ...overrides,
  }
}

beforeEach(() => {
  clients = []
  lastRegisterBody = null
  registerResponder = (body) => new Response(JSON.stringify(baseClient(body)), { status: 201, headers: jsonHeaders })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const method = init?.method ?? request?.method ?? 'GET'

      if (url.includes('/oauth2/register')) {
        const raw = request ? await request.text() : String(init?.body ?? '{}')
        lastRegisterBody = JSON.parse(raw) as NonNullable<typeof lastRegisterBody>
        const created = registerResponder(lastRegisterBody)
        if (created.status === 201) clients = [baseClient(lastRegisterBody)]
        return created
      }
      if (url.includes('/oauth2/clients')) {
        if (method === 'DELETE') {
          clients = clients.map((c) => ({ ...c, revoked_at: new Date().toISOString() }))
          return new Response(null, { status: 204 })
        }
        return new Response(JSON.stringify({ clients }), { status: 200, headers: jsonHeaders })
      }
      return new Response('not found', { status: 404 })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** Fills the register form's name, one valid redirect URL, and a scope. */
function fillValidRegisterForm() {
  fireEvent.change(screen.getByLabelText('App name'), { target: { value: 'Acme CRM sync' } })
  fireEvent.change(screen.getByLabelText('Redirect URL 1'), {
    target: { value: 'https://app.example.com/oauth/callback' },
  })
  const firstScope = screen.getAllByRole('checkbox')[0]
  expect(firstScope).toBeDefined()
  fireEvent.click(firstScope as HTMLElement)
}

test('non-admins get an admins-only state and no register action', async () => {
  renderWithProviders(<ConnectedAppsPanel />, {
    preloadedState: { auth: { role: 'member', status: 'authed', activeWorkspaceId: 'w1' } },
  })
  expect(await screen.findByText('Admins only')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /register an app/i })).not.toBeInTheDocument()
})

test('shows the empty state for an admin with no apps', async () => {
  renderWithProviders(<ConnectedAppsPanel />, { preloadedState: admin })
  expect(await screen.findByText('No connected apps yet')).toBeInTheDocument()
})

test('registering a confidential app reveals the secret exactly once and clears it on close', async () => {
  registerResponder = (body) =>
    new Response(JSON.stringify(baseClient(body, { client_secret: 'app_secret_shown_once_123' })), {
      status: 201,
      headers: jsonHeaders,
    })

  renderWithProviders(<ConnectedAppsPanel />, { preloadedState: admin })
  await screen.findByText('No connected apps yet')
  fireEvent.click(screen.getByRole('button', { name: /register an app/i }))

  fillValidRegisterForm()
  // Switch to a confidential client so a secret is issued.
  fireEvent.click(screen.getByRole('radio', { name: /confidential/i }))
  fireEvent.click(screen.getByRole('button', { name: /^register app$/i }))

  // The secret is shown once, behind the one-time warning; the client_id too.
  // Scope the id to the dialog: it also appears in the refetched list row beneath.
  expect(await screen.findByText('app_secret_shown_once_123')).toBeInTheDocument()
  expect(screen.getByText(/only time the client secret is shown/i)).toBeInTheDocument()
  expect(within(screen.getByRole('alertdialog')).getByText('app_generated_1')).toBeInTheDocument()
  expect(lastRegisterBody?.token_endpoint_auth_method).toBe('client_secret_basic')

  // Dismiss — the secret must be gone from the DOM (never re-derivable).
  fireEvent.click(screen.getByRole('button', { name: /^done$/i }))
  await waitFor(() => expect(screen.queryByText('app_secret_shown_once_123')).not.toBeInTheDocument())
  // The list refetched and shows the registered app.
  expect(await screen.findByText('Acme CRM sync')).toBeInTheDocument()
})

test('a public (PKCE) app shows the client_id and states there is no secret', async () => {
  renderWithProviders(<ConnectedAppsPanel />, { preloadedState: admin })
  await screen.findByText('No connected apps yet')
  fireEvent.click(screen.getByRole('button', { name: /register an app/i }))

  fillValidRegisterForm() // public is the default client type
  fireEvent.click(screen.getByRole('button', { name: /^register app$/i }))

  // The "no secret" copy is unique to the reveal step — a stable wait anchor.
  expect(await screen.findByText(/public \(pkce\) client, so there's no secret/i)).toBeInTheDocument()
  expect(within(screen.getByRole('alertdialog')).getByText('app_generated_1')).toBeInTheDocument()
  expect(lastRegisterBody?.token_endpoint_auth_method).toBe('none')
})

test('register is blocked and gives fast feedback on an invalid redirect URL', async () => {
  renderWithProviders(<ConnectedAppsPanel />, { preloadedState: admin })
  await screen.findByText('No connected apps yet')
  fireEvent.click(screen.getByRole('button', { name: /register an app/i }))

  fireEvent.change(screen.getByLabelText('App name'), { target: { value: 'Bad app' } })
  fireEvent.change(screen.getByLabelText('Redirect URL 1'), { target: { value: 'http://not-localhost.com/cb' } })
  const firstScope = screen.getAllByRole('checkbox')[0]
  fireEvent.click(firstScope as HTMLElement)

  fireEvent.click(screen.getByRole('button', { name: /^register app$/i }))

  expect(await screen.findByText(/use https, or http on localhost/i)).toBeInTheDocument()
  // Never hit the network with a bad URL.
  expect(lastRegisterBody).toBeNull()
})

test('register is disabled until a name, a redirect URL, and a scope are provided', async () => {
  renderWithProviders(<ConnectedAppsPanel />, { preloadedState: admin })
  await screen.findByText('No connected apps yet')
  fireEvent.click(screen.getByRole('button', { name: /register an app/i }))

  const submit = screen.getByRole('button', { name: /^register app$/i })
  expect(submit).toBeDisabled()

  fireEvent.change(screen.getByLabelText('App name'), { target: { value: 'App' } })
  expect(submit).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Redirect URL 1'), { target: { value: 'https://app.example.com/cb' } })
  expect(submit).toBeDisabled() // still no scope

  fireEvent.click(screen.getAllByRole('checkbox')[0] as HTMLElement)
  expect(submit).toBeEnabled()
})

test('revoking an app confirms and surfaces the outcome', async () => {
  clients = [
    {
      client_id: 'app_legacy_1',
      client_name: 'Legacy app',
      redirect_uris: ['https://legacy.example.com/cb'],
      grant_types: ['authorization_code'],
      response_types: ['code'],
      scope: 'contacts:read',
      client_type: 'public',
      token_endpoint_auth_method: 'none',
      created_at: new Date().toISOString(),
      revoked_at: null,
    },
  ]

  renderWithProviders(<ConnectedAppsPanel />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /revoke app legacy app/i }))
  fireEvent.click(await screen.findByRole('button', { name: /^revoke app$/i }))

  await waitFor(() =>
    expect(screen.getByRole('status')).toHaveTextContent(/“legacy app” was revoked/i),
  )
})
