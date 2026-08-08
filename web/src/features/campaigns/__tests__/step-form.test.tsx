import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { SequenceStep } from '../api'
import { StepForm } from '../step-form'

// StepForm's "Send test" row (edit mode only) fires the campaign-scoped
// test-send mutation — a worker-enqueued send, not an inline one, hence the
// "queued" copy rather than "sent". These tests lock the address default, the
// exact success/error copy, and that add mode never renders the control at all.

const jsonHeaders = { 'content-type': 'application/json' }

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

function makeStep(overrides: Partial<SequenceStep> = {}): SequenceStep {
  return { id: 'step-1', step_order: 1, delay_seconds: 0, subject: 'Hi', body_text: 'Hello', ...overrides }
}

let testSendResponder: () => Response
let authMeResponder: () => Response
let requests: Array<{ method: string; url: string; body: unknown }>

/** Signed in, so the form's `useEmailVerified` actually queries /auth/me. */
const AUTHED = { auth: { status: 'authed' as const, accessToken: 'token', userEmail: 'operator@inroad.dev' } }

beforeEach(() => {
  requests = []
  testSendResponder = () => jsonResponse({ queued: true }, 202)
  authMeResponder = () => jsonResponse({ user_id: 'u-1', email: 'operator@inroad.dev', email_verified: true })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()
      const body = isRequest ? await input.clone().json().catch(() => undefined) : undefined
      requests.push({ method, url, body })

      if (url.includes('/auth/me')) return authMeResponder()
      if (url.endsWith('/test-send') && method === 'POST') return testSendResponder()
      return jsonResponse({})
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('add mode (no step) never renders the Send test control', () => {
  renderWithProviders(
    <StepForm campaignId="c-1" isFirstStep onDone={vi.fn()} onCancel={vi.fn()} />,
  )
  expect(screen.queryByRole('button', { name: /send test/i })).not.toBeInTheDocument()
})

test('edit mode defaults the test address to the signed-in user\'s email', async () => {
  renderWithProviders(
    <StepForm campaignId="c-1" step={makeStep()} isFirstStep onDone={vi.fn()} onCancel={vi.fn()} />,
    { preloadedState: { auth: { userEmail: 'operator@inroad.dev' } } },
  )
  expect(await screen.findByRole('textbox', { name: /send test to/i })).toHaveValue('operator@inroad.dev')
})

test('sending a test posts the step id + address and shows the queued copy, not "sent"', async () => {
  renderWithProviders(
    <StepForm campaignId="c-1" step={makeStep()} isFirstStep onDone={vi.fn()} onCancel={vi.fn()} />,
    { preloadedState: { auth: { userEmail: 'operator@inroad.dev' } } },
  )

  fireEvent.click(await screen.findByRole('button', { name: /^send test$/i }))

  await waitFor(() =>
    expect(requests.some((r) => r.method === 'POST' && r.url.endsWith('/campaigns/c-1/test-send'))).toBe(true),
  )
  const req = requests.find((r) => r.url.endsWith('/test-send'))
  expect(req?.body).toEqual({ step_id: 'step-1', to: 'operator@inroad.dev' })

  expect(await screen.findByText('Test queued for operator@inroad.dev — it should arrive shortly.')).toBeInTheDocument()
  expect(screen.queryByText(/test sent/i)).not.toBeInTheDocument()
})

test('a custom address overrides the default and is the one sent + echoed', async () => {
  renderWithProviders(
    <StepForm campaignId="c-1" step={makeStep()} isFirstStep onDone={vi.fn()} onCancel={vi.fn()} />,
    { preloadedState: { auth: { userEmail: 'operator@inroad.dev' } } },
  )

  const addressInput = await screen.findByRole('textbox', { name: /send test to/i })
  fireEvent.change(addressInput, { target: { value: 'reviewer@inroad.dev' } })
  fireEvent.click(screen.getByRole('button', { name: /^send test$/i }))

  expect(await screen.findByText('Test queued for reviewer@inroad.dev — it should arrive shortly.')).toBeInTheDocument()
  const req = requests.find((r) => r.url.endsWith('/test-send'))
  expect(req?.body).toEqual({ step_id: 'step-1', to: 'reviewer@inroad.dev' })
})

test('a 422 test-send error surfaces "No enabled sender…" inline under the control', async () => {
  testSendResponder = () => jsonResponse({ error: 'no enabled sender with an active mailbox' }, 422)

  renderWithProviders(
    <StepForm campaignId="c-1" step={makeStep()} isFirstStep onDone={vi.fn()} onCancel={vi.fn()} />,
    { preloadedState: { auth: { userEmail: 'operator@inroad.dev' } } },
  )
  fireEvent.click(await screen.findByRole('button', { name: /^send test$/i }))

  expect(await screen.findByText(/no enabled sender with a connected mailbox/i)).toBeInTheDocument()
  expect(screen.queryByText(/test queued/i)).not.toBeInTheDocument()
})

test('a 429 test-send error surfaces the rate-limit copy', async () => {
  testSendResponder = () => jsonResponse({ error: 'too many test sends; try again shortly' }, 429)

  renderWithProviders(
    <StepForm campaignId="c-1" step={makeStep()} isFirstStep onDone={vi.fn()} onCancel={vi.fn()} />,
    { preloadedState: { auth: { userEmail: 'operator@inroad.dev' } } },
  )
  fireEvent.click(await screen.findByRole('button', { name: /^send test$/i }))

  expect(await screen.findByText(/too many test sends/i)).toBeInTheDocument()
})

// Email verification: POST /campaigns/{id}/test-send is behind
// `auth.RequireVerified`. Saving the step itself is not, so only this control
// is gated — over-gating would be its own bug.
test('an unverified account cannot send a test, and the control says why', async () => {
  authMeResponder = () => jsonResponse({ user_id: 'u-1', email: 'operator@inroad.dev', email_verified: false })

  renderWithProviders(
    <StepForm campaignId="c-1" step={makeStep()} isFirstStep onDone={vi.fn()} onCancel={vi.fn()} />,
    { preloadedState: AUTHED },
  )

  await waitFor(() =>
    expect(screen.getByRole('button', { name: /^send test$/i })).toHaveAttribute('aria-disabled', 'true'),
  )
  const button = screen.getByRole('button', { name: /^send test$/i })
  const hintId = button.getAttribute('aria-describedby')
  expect(document.getElementById(hintId ?? '')).toHaveTextContent(
    /Verify your email address to send a test email\./,
  )

  fireEvent.click(button)
  expect(requests.some((r) => r.url.endsWith('/test-send'))).toBe(false)

  // The step's own Save is untouched — the server doesn't gate it.
  expect(screen.getByRole('button', { name: /save step/i })).toBeEnabled()
})

test('a 403 email_not_verified test-send surfaces the verification copy', async () => {
  testSendResponder = () => jsonResponse({ error: 'email_not_verified' }, 403)

  renderWithProviders(
    <StepForm campaignId="c-1" step={makeStep()} isFirstStep onDone={vi.fn()} onCancel={vi.fn()} />,
    { preloadedState: { auth: { userEmail: 'operator@inroad.dev' } } },
  )
  fireEvent.click(await screen.findByRole('button', { name: /^send test$/i }))

  expect(await screen.findByText(/Verify your email address to send a test email\./i)).toBeInTheDocument()
  expect(screen.queryByText(/Couldn’t send the test email/i)).not.toBeInTheDocument()
})

test('the Send test button is disabled while the address field is empty', async () => {
  renderWithProviders(
    <StepForm campaignId="c-1" step={makeStep()} isFirstStep onDone={vi.fn()} onCancel={vi.fn()} />,
    { preloadedState: { auth: { userEmail: null } } },
  )
  const button = await screen.findByRole('button', { name: /^send test$/i })
  expect(button).toBeDisabled()

  fireEvent.change(screen.getByRole('textbox', { name: /send test to/i }), { target: { value: 'a@b.com' } })
  expect(button).toBeEnabled()
})
