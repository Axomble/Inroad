import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { LoginForm } from './login-form'

// A minimal PublicKeyCredential stand-in for the discoverable-login ceremony.
// `runAuthenticationCeremony` narrows the `navigator.credentials.get` result
// with `instanceof PublicKeyCredential`, so the resolved object must be an
// instance of the same (stubbed) global.
class FakePublicKeyCredential {
  id = 'cred-abc'
  type = 'public-key'
  rawId = new Uint8Array([1, 2, 3]).buffer
  authenticatorAttachment: string | null = 'platform'
  response = {
    clientDataJSON: new Uint8Array([4, 5, 6]).buffer,
    authenticatorData: new Uint8Array([7, 8, 9]).buffer,
    signature: new Uint8Array([10, 11, 12]).buffer,
    userHandle: null as ArrayBuffer | null,
  }
  getClientExtensionResults() {
    return {}
  }
}

const PASSKEY_BEGIN = {
  session_id: 'sess-login-1',
  publicKey: { challenge: 'AAEC', allowCredentials: [], userVerification: 'required' },
}

let getMock: ReturnType<typeof vi.fn>

/**
 * Turn on WebAuthn for a test: expose `PublicKeyCredential` (so the button
 * renders) and a `navigator.credentials.get` that resolves the fake credential.
 * Mirrors how the passkeys-settings tests enable `create`.
 */
function enablePasskeys() {
  getMock = vi.fn().mockResolvedValue(new FakePublicKeyCredential())
  vi.stubGlobal('PublicKeyCredential', FakePublicKeyCredential)
  Object.defineProperty(navigator, 'credentials', {
    configurable: true,
    value: { get: getMock, create: vi.fn() },
  })
}

// LoginForm uses the router's useNavigate + Link + the `/` route's search
// (`return_to`); stub them and capture navigation. Default search is empty, so
// completeLogin falls through to the operational overview.
const navigate = vi.fn()
let loginSearch: { return_to?: string } = {}
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  getRouteApi: () => ({ useSearch: () => loginSearch }),
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
let passkeyBeginResponder: () => Response
let passkeyFinishResponder: () => Response

beforeEach(() => {
  navigate.mockClear()
  loginSearch = {}
  loginResponder = () => new Response(JSON.stringify(SESSION), { status: 200, headers: jsonHeaders })
  verifyResponder = () => new Response(JSON.stringify(SESSION), { status: 200, headers: jsonHeaders })
  otpStartResponder = () => new Response(JSON.stringify({ status: 'ok' }), { status: 200, headers: jsonHeaders })
  otpVerifyResponder = () => new Response(JSON.stringify(SESSION), { status: 200, headers: jsonHeaders })
  passkeyBeginResponder = () => new Response(JSON.stringify(PASSKEY_BEGIN), { status: 200, headers: jsonHeaders })
  passkeyFinishResponder = () => new Response(JSON.stringify(SESSION), { status: 200, headers: jsonHeaders })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      if (url.includes('/auth/2fa/verify')) return verifyResponder()
      if (url.includes('/auth/email-otp/start')) return otpStartResponder()
      if (url.includes('/auth/email-otp/verify')) return otpVerifyResponder()
      if (url.includes('/auth/passkeys/login/begin')) return passkeyBeginResponder()
      if (url.includes('/auth/passkeys/login/finish')) return passkeyFinishResponder()
      if (url.includes('/auth/login')) return loginResponder()
      return new Response(null, { status: 404 })
    }),
  )
})

const ORIGINAL_LOCATION = window.location

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  // A couple of tests swap in a stub `window.location` to capture `assign`; put
  // the real one back so it doesn't leak into later tests.
  Object.defineProperty(window, 'location', { configurable: true, value: ORIGINAL_LOCATION })
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

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app' }))
})

// Swap in a stub `window.location` (keeping the real origin) whose `assign` we
// can capture, so the open-redirect guard is exercised against the real
// same-origin comparison. afterEach restores the real location.
function stubAssign() {
  const assign = vi.fn()
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...window.location, assign },
  })
  return assign
}

// Legit same-origin `return_to`s: resumed via a full navigation to the
// NORMALIZED same-origin path (an SPA path, and the API's /oauth2/authorize).
test.each([
  '/oauth/consent?consent_id=c-1',
  '/oauth2/authorize?client_id=abc&state=xyz&scope=mailboxes.read',
])('a safe return_to (%s) resumes there via a full navigation, not the SPA router', async (returnTo) => {
  loginSearch = { return_to: returnTo }
  const assign = stubAssign()

  renderWithProviders(<LoginForm />)
  fillCredentials()

  await waitFor(() => expect(assign).toHaveBeenCalledWith(returnTo))
  // The SPA router is NOT used for the resume (the target may be a non-SPA path).
  expect(navigate).not.toHaveBeenCalledWith({ to: '/app' })
})

// Every open-redirect bypass family must be rejected: protocol-relative,
// backslash (WHATWG-normalized to `//` — the bug that shipped), absolute, and
// scheme (`javascript:` / `data:`) URLs. None may reach `assign`; login must
// fall back to the default post-login route. (`'/\\evil.com'` is the literal
// `/\evil.com`; `'\\\\evil.com'` is `\\evil.com`.)
test.each([
  '//evil.com',
  '/\\evil.com',
  '/\\/evil.com',
  'https://evil.com',
  'http://evil.com',
  'javascript:alert(1)',
  'data:text/html,x',
  '\\\\evil.com',
])('an unsafe return_to (%j) is ignored (open-redirect guard) and falls back to the app', async (returnTo) => {
  loginSearch = { return_to: returnTo }
  const assign = stubAssign()

  renderWithProviders(<LoginForm />)
  fillCredentials()

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app' }))
  // Never handed off to the browser at all — not to the malicious target, and
  // not to any normalized off-origin path.
  expect(assign).not.toHaveBeenCalled()
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

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app' }))
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

  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app' }))
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

  // The 120s Retry-After above reaches the copy because the shared base query
  // folds the header onto the error payload (store/empty-api.ts); rounded up,
  // that reads as two minutes rather than a vague "wait a moment".
  expect(await screen.findByText(/too many attempts.*try again in about 2 minutes/i)).toBeInTheDocument()
  // Still on the request step — no code field yet.
  expect(screen.queryByLabelText(/sign-in code/i)).not.toBeInTheDocument()
})

// ── Passkey (discoverable) login (F3) ─────────────────────────────────────────

const passkeyButton = () => screen.queryByRole('button', { name: /sign in with a passkey/i })

test('passkey login: begin → ceremony → finish mints a session and redirects', async () => {
  enablePasskeys()
  renderWithProviders(<LoginForm />)

  fireEvent.click(screen.getByRole('button', { name: /sign in with a passkey/i }))

  // The browser ceremony ran with decoded (binary) assertion options…
  await waitFor(() => expect(getMock).toHaveBeenCalledTimes(1))
  const passed = getMock.mock.calls[0]?.[0] as { publicKey: PublicKeyCredentialRequestOptions }
  expect(passed.publicKey.challenge).toBeInstanceOf(ArrayBuffer)

  // …and lands on the same session/redirect outcome as a password login.
  await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/app' }))
})

test('passkey login: a 501 from begin retires the passkey button', async () => {
  passkeyBeginResponder = () =>
    new Response(JSON.stringify({ error: 'not_configured' }), { status: 501, headers: jsonHeaders })
  enablePasskeys()
  renderWithProviders(<LoginForm />)

  fireEvent.click(screen.getByRole('button', { name: /sign in with a passkey/i }))

  await waitFor(() => expect(passkeyButton()).not.toBeInTheDocument())
  // The ceremony never ran and no session was minted.
  expect(getMock).not.toHaveBeenCalled()
  expect(navigate).not.toHaveBeenCalled()
})

test('passkey login: a 501 from finish retires the passkey button', async () => {
  passkeyFinishResponder = () =>
    new Response(JSON.stringify({ error: 'not_configured' }), { status: 501, headers: jsonHeaders })
  enablePasskeys()
  renderWithProviders(<LoginForm />)

  fireEvent.click(screen.getByRole('button', { name: /sign in with a passkey/i }))

  await waitFor(() => expect(getMock).toHaveBeenCalledTimes(1))
  await waitFor(() => expect(passkeyButton()).not.toBeInTheDocument())
  expect(navigate).not.toHaveBeenCalled()
})

test('passkey login: a user-cancelled ceremony shows an inline error, keeps the button, no stuck spinner', async () => {
  enablePasskeys()
  getMock.mockRejectedValueOnce(new DOMException('cancelled', 'NotAllowedError'))
  renderWithProviders(<LoginForm />)

  fireEvent.click(screen.getByRole('button', { name: /sign in with a passkey/i }))

  expect(await screen.findByText(/cancelled or timed out/i)).toBeInTheDocument()
  // Button is still present and interactive (busy resolved), and no redirect.
  const button = passkeyButton()
  expect(button).toBeInTheDocument()
  expect(button).toBeEnabled()
  expect(navigate).not.toHaveBeenCalled()
})
