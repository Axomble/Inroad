import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { UnverifiedBanner } from '../unverified-banner'

// The app-wide prompt for an unconfirmed address: it must appear only when
// /auth/me says the address is unverified, name what's blocked, and give both
// outcomes of a resend as text (never colour alone).

const jsonHeaders = { 'content-type': 'application/json' }

let authMeResponder: () => Response
let resendResponder: () => Response
let requests: string[]

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

/** Signed in — the banner skips its query entirely when it isn't. */
const AUTHED = { auth: { status: 'authed' as const, accessToken: 'token' } }

beforeEach(() => {
  requests = []
  authMeResponder = () => jsonResponse({ user_id: 'u-1', email: 'me@company.com', email_verified: false })
  resendResponder = () => jsonResponse({}, 202)

  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      requests.push(url)
      if (url.includes('/auth/verify-email/resend')) return Promise.resolve(resendResponder())
      return Promise.resolve(authMeResponder())
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('an unverified account sees what is blocked and how to fix it', async () => {
  renderWithProviders(<UnverifiedBanner />, { preloadedState: AUTHED })

  const banner = await screen.findByRole('status')
  expect(banner).toHaveTextContent(/connecting a mailbox, launching a campaign, and test sends/i)
  expect(screen.getByRole('button', { name: /resend email/i })).toBeInTheDocument()
})

test('a verified account sees nothing', async () => {
  authMeResponder = () => jsonResponse({ user_id: 'u-1', email: 'me@company.com', email_verified: true })
  renderWithProviders(<UnverifiedBanner />, { preloadedState: AUTHED })

  await waitFor(() => expect(requests.some((url) => url.includes('/auth/me'))).toBe(true))
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
})

test('a successful resend confirms it was sent', async () => {
  renderWithProviders(<UnverifiedBanner />, { preloadedState: AUTHED })

  fireEvent.click(await screen.findByRole('button', { name: /resend email/i }))

  expect(await screen.findByText(/Verification email sent — check your inbox/i)).toBeInTheDocument()
  expect(requests.some((url) => url.includes('/auth/verify-email/resend'))).toBe(true)
})

test('a failed resend says so and offers a retry', async () => {
  resendResponder = () => jsonResponse({ error: 'smtp unavailable' }, 500)
  renderWithProviders(<UnverifiedBanner />, { preloadedState: AUTHED })

  fireEvent.click(await screen.findByRole('button', { name: /resend email/i }))

  expect(await screen.findByText(/Couldn’t send the email/i)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /try again/i })).toBeInTheDocument()
})

test('the resend button is disabled while the request is in flight', async () => {
  let release: () => void = () => {}
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  resendResponder = () => jsonResponse({}, 202)
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      if (url.includes('/auth/verify-email/resend')) {
        await gate
        return resendResponder()
      }
      return authMeResponder()
    }),
  )

  renderWithProviders(<UnverifiedBanner />, { preloadedState: AUTHED })
  fireEvent.click(await screen.findByRole('button', { name: /resend email/i }))

  expect(await screen.findByRole('button', { name: /sending…/i })).toBeDisabled()
  release()
  expect(await screen.findByText(/Verification email sent/i)).toBeInTheDocument()
})
