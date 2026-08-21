import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { RegisterForm } from '../register-form'

const navigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  Link: ({ to, children, ...props }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

const jsonHeaders = { 'content-type': 'application/json' }
const SESSION = {
  access_token: 'tok-abc',
  expires_in: 900,
  user_id: 'u-1',
  active_workspace_id: 'w-1',
  role: 'owner',
  memberships: [],
}

const ORIGINAL_LOCATION = window.location
let assign: ReturnType<typeof vi.fn>

beforeEach(() => {
  navigate.mockClear()
  assign = vi.fn()
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...ORIGINAL_LOCATION, origin: ORIGINAL_LOCATION.origin, assign },
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify(SESSION), { status: 200, headers: jsonHeaders })),
  )
})

afterEach(() => {
  Object.defineProperty(window, 'location', { configurable: true, value: ORIGINAL_LOCATION })
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('Google leads the screen, above the email fields', () => {
  renderWithProviders(<RegisterForm />)

  const google = screen.getByRole('button', { name: /sign up with google/i })
  expect(google.compareDocumentPosition(screen.getByLabelText('Workspace name'))).toBe(
    Node.DOCUMENT_POSITION_FOLLOWING,
  )
})

test('signing up with Google is a redirect, not a form submission', () => {
  renderWithProviders(<RegisterForm />)

  fireEvent.click(screen.getByRole('button', { name: /sign up with google/i }))

  expect(String(assign.mock.calls[0]?.[0])).toContain('/auth/oauth/google/start')
  // Nothing was registered client-side; the workspace is created server-side and
  // named in the onboarding overlay.
  expect(navigate).not.toHaveBeenCalled()
})

test('the email path still registers and enters the app', async () => {
  renderWithProviders(<RegisterForm />)

  fireEvent.change(screen.getByLabelText('Workspace name'), { target: { value: 'Acme Outbound' } })
  fireEvent.change(screen.getByLabelText('Work email'), { target: { value: 'me@acme.com' } })
  fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'hunter2hunter2' } })
  fireEvent.click(screen.getByRole('button', { name: /create workspace/i }))

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app' }))
})
