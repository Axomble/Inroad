import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi, type Mock } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { VerifiedGateButton } from './verified-gate-button'

// The gate every server-gated action goes through. `gated-action-button.test.tsx`
// owns the accessibility contract (aria-disabled, focusable, aria-describedby);
// these lock what this wrapper adds: where "blocked" comes from, and — the
// non-obvious one — that an unknown answer fails OPEN.

const jsonHeaders = { 'content-type': 'application/json' }

let authMeResponder: () => Promise<Response>
let onClick: Mock<() => void>

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

/** Signed in — the hook skips its query entirely when nobody is. */
const AUTHED = { auth: { status: 'authed' as const, accessToken: 'token' } }

function verification(emailVerified: boolean) {
  return () => Promise.resolve(jsonResponse({ user_id: 'u-1', email: 'me@co.dev', email_verified: emailVerified }))
}

beforeEach(() => {
  onClick = vi.fn()
  authMeResponder = verification(true)
  vi.stubGlobal(
    'fetch',
    vi.fn(() => authMeResponder()),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function renderButton(preloadedState?: { auth: Partial<typeof AUTHED.auth> }) {
  return renderWithProviders(
    <VerifiedGateButton action="connect a mailbox" onClick={() => onClick()}>
      Connect mailbox
    </VerifiedGateButton>,
    { preloadedState },
  )
}

test('an unverified account gets a blocked button explaining the action it names', async () => {
  authMeResponder = verification(false)
  renderButton(AUTHED)

  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Connect mailbox' })).toHaveAttribute('aria-disabled', 'true'),
  )
  const button = screen.getByRole('button', { name: 'Connect mailbox' })
  const hintId = button.getAttribute('aria-describedby')
  expect(document.getElementById(hintId ?? '')).toHaveTextContent(
    'Verify your email address to connect a mailbox.',
  )

  fireEvent.click(button)
  expect(onClick).not.toHaveBeenCalled()
})

test('a verified account gets an ordinary working button', async () => {
  renderButton(AUTHED)

  // Wait for the answer to land, so this can't pass on the fail-open default.
  await waitFor(() => expect(fetch).toHaveBeenCalled())
  await waitFor(() => expect(screen.getByRole('button')).not.toHaveAttribute('aria-disabled'))

  fireEvent.click(screen.getByRole('button', { name: 'Connect mailbox' }))
  expect(onClick).toHaveBeenCalledTimes(1)
})

test('an in-flight verification answer fails open rather than briefly disabling the button', async () => {
  // Never resolves: this is the first paint after mount, before /auth/me answers.
  authMeResponder = () => new Promise<Response>(() => {})
  renderButton(AUTHED)

  const button = screen.getByRole('button', { name: 'Connect mailbox' })
  expect(button).not.toHaveAttribute('aria-disabled')
  fireEvent.click(button)
  expect(onClick).toHaveBeenCalledTimes(1)
})

test('a signed-out user is not gated — there is no account to verify', () => {
  // `idle`/`anon` skips the query entirely, so nothing is ever blocked here.
  renderButton()

  expect(screen.getByRole('button', { name: 'Connect mailbox' })).not.toHaveAttribute('aria-disabled')
  expect(fetch).not.toHaveBeenCalled()
})
