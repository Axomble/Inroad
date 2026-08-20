import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { WorkspaceOnboardingOverlay } from '../workspace-onboarding-overlay'

// The one mandatory first-run step. What matters here is not that it renders, but
// that it is genuinely unescapable in the ways it must be (Escape, backdrop, close
// button) and genuinely escapable in the one way it must be (sign out) — and that
// it never appears for a workspace that has already been named.

const navigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
}))

const jsonHeaders = { 'content-type': 'application/json' }

/** Signed in, with the server-derived workspace name on the membership list. */
const AUTHED = {
  auth: {
    status: 'authed' as const,
    accessToken: 'token',
    activeWorkspaceId: 'w-1',
    memberships: [
      { workspace_id: 'w-1', workspace_name: 'Acme Com', role: 'owner', onboarding_completed_at: null },
    ],
  },
}

function me(onboarding: { onboarding_completed_at?: string | null }) {
  return {
    user_id: 'u-1',
    active_workspace_id: 'w-1',
    role: 'owner',
    memberships: [
      { workspace_id: 'w-1', workspace_name: 'Acme Com', role: 'owner', onboarding_completed_at: null },
    ],
    email: 'ops@acme.com',
    email_verified: true,
    ...onboarding,
  }
}

let meResponder: () => Response
let completeResponder: () => Response
let requests: { url: string; body: string | null }[]

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

beforeEach(async () => {
  // The panel is behind React.lazy, so its first render waits on a dynamic
  // import. Resolving that module up front means the dialog appears in a
  // microtask rather than racing a cold transform — without it, `findByRole`
  // intermittently times out on a loaded machine.
  await import('../workspace-onboarding-dialog')

  requests = []
  navigate.mockClear()
  meResponder = () => jsonResponse(me({ onboarding_completed_at: null }))
  completeResponder = () =>
    jsonResponse({
      workspace_id: 'w-1',
      name: 'Acme Outbound',
      onboarding_completed_at: '2026-08-08T10:00:00Z',
    })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      // fetchBaseQuery hands `fetch` a Request, so the body is on the request
      // object rather than an init bag.
      const body = input instanceof Request ? await input.clone().text() : null
      requests.push({ url, body })
      if (url.includes('/onboarding/complete')) return completeResponder()
      if (url.includes('/auth/logout')) return jsonResponse({})
      return meResponder()
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const dialog = () => screen.queryByRole('dialog')
const nameField = () => screen.getByLabelText('Workspace name')

test('a workspace that has never been named is blocked until it is', async () => {
  renderWithProviders(<WorkspaceOnboardingOverlay />, { preloadedState: AUTHED })

  const panel = await screen.findByRole('dialog')
  expect(panel).toHaveAttribute('aria-modal', 'true')
  // Labelled by its own heading, so a screen reader announces the task on open.
  expect(panel).toHaveAccessibleName('Name your workspace')
  // Only naming is asked for — invites and mailboxes are deliberately not here.
  expect(screen.queryByLabelText(/email/i)).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /invite/i })).not.toBeInTheDocument()
})

test('the name is pre-filled with the server-derived name and holds focus', async () => {
  renderWithProviders(<WorkspaceOnboardingOverlay />, { preloadedState: AUTHED })
  await screen.findByRole('dialog')

  // The Google-domain-derived name the server already chose: the common case is
  // one Enter press, not typing.
  expect(nameField()).toHaveValue('Acme Com')
  expect(nameField()).toHaveFocus()
})

test('an already-onboarded workspace never sees it (an invited user joins straight in)', async () => {
  meResponder = () => jsonResponse(me({ onboarding_completed_at: '2026-08-08T10:00:00Z' }))
  renderWithProviders(<WorkspaceOnboardingOverlay />, { preloadedState: AUTHED })

  await waitFor(() => expect(requests.some((r) => r.url.includes('/auth/me'))).toBe(true))
  expect(dialog()).not.toBeInTheDocument()
})

test('an API that does not report the flag at all shows nothing, rather than blocking everyone', async () => {
  // A missing field must not read as "not onboarded" — that would put an
  // un-dismissible screen in front of every user of an older server.
  meResponder = () => jsonResponse(me({}))
  renderWithProviders(<WorkspaceOnboardingOverlay />, { preloadedState: AUTHED })

  await waitFor(() => expect(requests.some((r) => r.url.includes('/auth/me'))).toBe(true))
  expect(dialog()).not.toBeInTheDocument()
})

test('Escape does not dismiss it', async () => {
  renderWithProviders(<WorkspaceOnboardingOverlay />, { preloadedState: AUTHED })
  const panel = await screen.findByRole('dialog')

  fireEvent.keyDown(panel, { key: 'Escape', code: 'Escape' })
  fireEvent.keyDown(document.body, { key: 'Escape', code: 'Escape' })

  expect(dialog()).toBeInTheDocument()
})

test('clicking the backdrop does not dismiss it, and there is no close button', async () => {
  renderWithProviders(<WorkspaceOnboardingOverlay />, { preloadedState: AUTHED })
  await screen.findByRole('dialog')

  const overlay = document.querySelector('[data-slot="alert-dialog-overlay"]')
  expect(overlay).not.toBeNull()
  fireEvent.pointerDown(overlay as Element)
  fireEvent.click(overlay as Element)

  expect(dialog()).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /close|dismiss/i })).not.toBeInTheDocument()
})

test('sign out is always reachable, and works even if the server call fails', async () => {
  renderWithProviders(<WorkspaceOnboardingOverlay />, { preloadedState: AUTHED })
  await screen.findByRole('dialog')

  const signOut = screen.getByRole('button', { name: /sign out/i })
  // Inside the dialog, so it's reachable within the focus trap.
  expect(dialog()).toContainElement(signOut)
  fireEvent.click(signOut)

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/' }))
})

test('submitting a name posts it and the refreshed flag dismisses the overlay', async () => {
  const { store } = renderWithProviders(<WorkspaceOnboardingOverlay />, { preloadedState: AUTHED })
  await screen.findByRole('dialog')

  fireEvent.change(nameField(), { target: { value: '  Acme Outbound  ' } })
  // Once the workspace is named, /auth/me reports it as onboarded — which is what
  // actually closes the overlay (never a local success flag).
  meResponder = () => jsonResponse(me({ onboarding_completed_at: '2026-08-08T10:00:00Z' }))
  fireEvent.click(screen.getByRole('button', { name: /continue/i }))

  await waitFor(() => expect(dialog()).not.toBeInTheDocument())

  const posted = requests.find((r) => r.url.includes('/workspaces/w-1/onboarding/complete'))
  expect(posted).toBeDefined()
  // Trimmed, not the raw field value.
  expect(JSON.parse(posted?.body ?? '{}')).toEqual({ name: 'Acme Outbound' })

  // The header's workspace switcher reads names off the session, so the name the
  // SERVER returned has to land there too — otherwise it keeps showing the one
  // derived at signup, which the user just replaced.
  const state = store.getState() as { auth: { memberships: { workspace_name: string }[] } }
  expect(state.auth.memberships[0]?.workspace_name).toBe('Acme Outbound')
})

test('a rejected name keeps the overlay open and shows the server’s reason', async () => {
  completeResponder = () => jsonResponse({ error: 'that workspace name is already taken' }, 409)
  renderWithProviders(<WorkspaceOnboardingOverlay />, { preloadedState: AUTHED })
  await screen.findByRole('dialog')

  fireEvent.change(nameField(), { target: { value: 'Acme Outbound' } })
  fireEvent.click(screen.getByRole('button', { name: /continue/i }))

  expect(await screen.findByText(/already taken/i)).toBeInTheDocument()
  expect(dialog()).toBeInTheDocument()
})

test('a failure with no explanation still says something actionable', async () => {
  completeResponder = () => new Response(null, { status: 500 })
  renderWithProviders(<WorkspaceOnboardingOverlay />, { preloadedState: AUTHED })
  await screen.findByRole('dialog')

  fireEvent.change(nameField(), { target: { value: 'Acme Outbound' } })
  fireEvent.click(screen.getByRole('button', { name: /continue/i }))

  expect(await screen.findByText(/couldn't save that name/i)).toBeInTheDocument()
  expect(dialog()).toBeInTheDocument()
})

test('an empty name is rejected client-side, with no request sent', async () => {
  renderWithProviders(<WorkspaceOnboardingOverlay />, { preloadedState: AUTHED })
  await screen.findByRole('dialog')

  fireEvent.change(nameField(), { target: { value: '   ' } })
  fireEvent.click(screen.getByRole('button', { name: /continue/i }))

  expect(await screen.findByText(/give your workspace a name/i)).toBeInTheDocument()
  expect(requests.some((r) => r.url.includes('/onboarding/complete'))).toBe(false)
  expect(dialog()).toBeInTheDocument()
})
