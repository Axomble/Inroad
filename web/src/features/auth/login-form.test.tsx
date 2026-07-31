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

// Per-test overridable responders for the auth endpoints in play.
let loginResponder: () => Response
let verifyResponder: () => Response
let otpStartResponder: () => Response
let otpVerifyResponder: () => Response

beforeEach(() => {
  navigate.mockClear()
  loginResponder = () => new Response(JSON.stringify(SESSION), { status: 200, headers: jsonHeaders })
  verifyResponder = () => new Response(JSON.stringify(SESSION), { status: 200, headers: jsonHeaders })
  otpStartResponder = () => new Response(JSON.stringify({ status: 'ok' }), { status: 200, headers: jsonHeaders })
  otpVerifyResponder = () => new Response(JSON.stringify(SESSION), { status: 200, headers: jsonHeaders })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      if (url.includes('/auth/2fa/verify')) return verifyResponder()
      if (url.includes('/auth/email-otp/start')) return otpStartResponder()
      if (url.includes('/auth/email-otp/verify')) return otpVerifyResponder()
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

// ── Email-OTP login (F3) ──────────────────────────────────────────────────────

function openEmailCodeStep() {
  fireEvent.click(screen.getByRole('button', { name: /sign in with an email code/i }))
}

test('email-code: start → verify → navigates to the app on a session', async () => {
  renderWithProviders(<LoginForm />)
  openEmailCodeStep()

  fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'me@company.com' } })
  fireEvent.click(screen.getByRole('button', { name: /send code/i }))

  // Anti-enumeration: the same generic acknowledgement regardless of the account.
  expect(await screen.findByText(/if an account exists/i)).toBeInTheDocument()

  fireEvent.change(screen.getByLabelText(/sign-in code/i), { target: { value: '123456' } })
  fireEvent.click(screen.getByRole('button', { name: /^verify$/i }))

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app/mailboxes' }))
})

test('email-code: a 2FA-required verify routes into the shared 2FA challenge, not a session', async () => {
  otpVerifyResponder = () =>
    new Response(JSON.stringify({ two_factor_required: true, challenge: 'chal-otp' }), {
      status: 200,
      headers: jsonHeaders,
    })

  renderWithProviders(<LoginForm />)
  openEmailCodeStep()

  fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'me@company.com' } })
  fireEvent.click(screen.getByRole('button', { name: /send code/i }))
  fireEvent.change(await screen.findByLabelText(/sign-in code/i), { target: { value: '123456' } })
  fireEvent.click(screen.getByRole('button', { name: /^verify$/i }))

  // Lands on the SAME 2FA challenge step the password path uses, not the app.
  expect(await screen.findByText('Enter your code')).toBeInTheDocument()
  expect(navigate).not.toHaveBeenCalled()
})

test('email-code: a rejected code shows an inline retry message', async () => {
  otpVerifyResponder = () =>
    new Response(JSON.stringify({ error: 'invalid_code' }), { status: 401, headers: jsonHeaders })

  renderWithProviders(<LoginForm />)
  openEmailCodeStep()

  fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'me@company.com' } })
  fireEvent.click(screen.getByRole('button', { name: /send code/i }))
  fireEvent.change(await screen.findByLabelText(/sign-in code/i), { target: { value: '000000' } })
  fireEvent.click(screen.getByRole('button', { name: /^verify$/i }))

  expect(await screen.findByText(/that code didn't work/i)).toBeInTheDocument()
  expect(navigate).not.toHaveBeenCalled()
})

test('email-code: a rate-limited start surfaces a clear too-many-attempts message', async () => {
  otpStartResponder = () =>
    new Response(JSON.stringify({ error: 'rate_limited' }), {
      status: 429,
      headers: { ...jsonHeaders, 'retry-after': '120' },
    })

  renderWithProviders(<LoginForm />)
  openEmailCodeStep()

  fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'me@company.com' } })
  fireEvent.click(screen.getByRole('button', { name: /send code/i }))

  expect(await screen.findByText(/too many attempts/i)).toBeInTheDocument()
  // Still on the request step — no code field yet.
  expect(screen.queryByLabelText(/sign-in code/i)).not.toBeInTheDocument()
})
