import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { LoginForm } from './login-form'

// LoginForm uses the router's useNavigate + Link; stub them and capture navigation.
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

// Per-test overridable responders for the two auth endpoints in play.
let loginResponder: () => Response
let verifyResponder: () => Response

beforeEach(() => {
  navigate.mockClear()
  loginResponder = () => new Response(JSON.stringify(SESSION), { status: 200, headers: jsonHeaders })
  verifyResponder = () => new Response(JSON.stringify(SESSION), { status: 200, headers: jsonHeaders })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      if (url.includes('/auth/2fa/verify')) return verifyResponder()
      if (url.includes('/auth/login')) return loginResponder()
      return new Response(null, { status: 404 })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function fillCredentials() {
  fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'me@company.com' } })
  fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'hunter2' } })
  fireEvent.click(screen.getByRole('button', { name: /log in/i }))
}

test('renders email and password fields', () => {
  renderWithProviders(<LoginForm />)
  expect(screen.getByLabelText('Email')).toBeInTheDocument()
  expect(screen.getByLabelText('Password')).toBeInTheDocument()
})

test('a non-2FA login stores the session and redirects', async () => {
  renderWithProviders(<LoginForm />)
  fillCredentials()

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app/mailboxes' }))
})

test('a 2FA-required login transitions to the challenge step, then verifies to a session', async () => {
  loginResponder = () =>
    new Response(JSON.stringify({ two_factor_required: true, challenge: 'chal-123' }), {
      status: 200,
      headers: jsonHeaders,
    })

  renderWithProviders(<LoginForm />)
  fillCredentials()

  // The challenge step replaces the credentials form.
  expect(await screen.findByText('Enter your code')).toBeInTheDocument()
  expect(navigate).not.toHaveBeenCalled()

  fireEvent.change(screen.getByLabelText(/authentication code/i), { target: { value: '123456' } })
  fireEvent.click(screen.getByRole('button', { name: /^verify$/i }))

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app/mailboxes' }))
})

test('a wrong code on the challenge step shows an error and stays put', async () => {
  loginResponder = () =>
    new Response(JSON.stringify({ two_factor_required: true, challenge: 'chal-123' }), {
      status: 200,
      headers: jsonHeaders,
    })
  verifyResponder = () => new Response(JSON.stringify({ error: 'invalid_code' }), { status: 401, headers: jsonHeaders })

  renderWithProviders(<LoginForm />)
  fillCredentials()

  await screen.findByText('Enter your code')
  fireEvent.change(screen.getByLabelText(/authentication code/i), { target: { value: '000000' } })
  fireEvent.click(screen.getByRole('button', { name: /^verify$/i }))

  expect(await screen.findByText(/that code didn't work/i)).toBeInTheDocument()
  expect(navigate).not.toHaveBeenCalled()
  // Still on the challenge step.
  expect(screen.getByText('Enter your code')).toBeInTheDocument()
})

test('an expired challenge sends the user back to the login step with a message', async () => {
  loginResponder = () =>
    new Response(JSON.stringify({ two_factor_required: true, challenge: 'chal-123' }), {
      status: 200,
      headers: jsonHeaders,
    })
  verifyResponder = () =>
    new Response(JSON.stringify({ error: 'challenge_expired' }), { status: 401, headers: jsonHeaders })

  renderWithProviders(<LoginForm />)
  fillCredentials()

  await screen.findByText('Enter your code')
  fireEvent.change(screen.getByLabelText(/authentication code/i), { target: { value: '123456' } })
  fireEvent.click(screen.getByRole('button', { name: /^verify$/i }))

  // Back on the credentials step with an explanatory notice.
  expect(await screen.findByRole('status')).toHaveTextContent(/expired.*sign in again/i)
  expect(screen.getByLabelText('Email')).toBeInTheDocument()
})

test('the recovery-code toggle switches the input label', async () => {
  loginResponder = () =>
    new Response(JSON.stringify({ two_factor_required: true, challenge: 'chal-123' }), {
      status: 200,
      headers: jsonHeaders,
    })

  renderWithProviders(<LoginForm />)
  fillCredentials()

  await screen.findByText('Enter your code')
  fireEvent.click(screen.getByRole('button', { name: /use a recovery code/i }))
  expect(screen.getByLabelText(/recovery code/i)).toBeInTheDocument()
})

test('a throttled login (429) surfaces a back-off message', async () => {
  loginResponder = () => new Response(JSON.stringify({ error: 'too_many' }), { status: 429, headers: jsonHeaders })

  renderWithProviders(<LoginForm />)
  fillCredentials()

  expect(await screen.findByText(/too many sign-in attempts/i)).toBeInTheDocument()
  expect(navigate).not.toHaveBeenCalled()
})
