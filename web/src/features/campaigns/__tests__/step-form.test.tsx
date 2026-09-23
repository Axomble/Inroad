import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { replaceText, warmRichTextEditors } from '@/test/rich-text'
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
let customFieldsResponder: () => Response
let requests: Array<{ method: string; url: string; body: unknown }>

/** Signed in, so the form's `useEmailVerified` actually queries /auth/me. */
const AUTHED = { auth: { status: 'authed' as const, accessToken: 'token', userEmail: 'operator@inroad.dev' } }

// The subject and body editors are lazy chunks — resolve them up front so
// findBy* never races a cold dynamic import.
beforeAll(async () => {
  await warmRichTextEditors()
}, 30_000)

beforeEach(() => {
  requests = []
  testSendResponder = () => jsonResponse({ queued: true }, 202)
  authMeResponder = () => jsonResponse({ user_id: 'u-1', email: 'operator@inroad.dev', email_verified: true })
  customFieldsResponder = () => jsonResponse([])

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const url = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()
      const body = isRequest ? await input.clone().json().catch(() => undefined) : undefined
      requests.push({ method, url, body })

      if (url.includes('/auth/me')) return authMeResponder()
      if (url.endsWith('/custom-fields')) return customFieldsResponder()
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

// --- Merge fields -----------------------------------------------------------

function customField(key: string, archived = false) {
  return { id: `cf-${key}`, key, label: key, type: 'text', options: [], created_at: '', archived, archived_at: null }
}

async function submitStep() {
  fireEvent.click(screen.getByRole('button', { name: /^(save|add) step$/i }))
}

const putRequests = () => requests.filter((r) => r.method === 'PUT' && r.url.endsWith('/campaigns/c-1/steps/step-1'))

test('an existing step loads its merge fields as chips in both subject and body', async () => {
  renderWithProviders(
    <StepForm
      campaignId="c-1"
      step={makeStep({ subject: 'Quick one, {{first_name}}', body_text: 'Hi {{first_name}}, how is {{company}}?' })}
      isFirstStep
      onDone={vi.fn()}
      onCancel={vi.fn()}
    />,
  )
  const subject = await screen.findByRole('textbox', { name: 'Subject' })
  const body = await screen.findByRole('textbox', { name: 'Body' })
  expect(await within(subject).findByLabelText(/^\{\{first_name\}\} — Merge field/)).toBeInTheDocument()
  expect(await within(body).findAllByLabelText(/Merge field/)).toHaveLength(2)
})

test('an unknown merge field is flagged before save and blocks it', async () => {
  const onDone = vi.fn()
  renderWithProviders(
    <StepForm
      campaignId="c-1"
      step={makeStep({ body_text: 'Hi {{firstname}}' })}
      isFirstStep
      onDone={onDone}
      onCancel={vi.fn()}
    />,
  )
  const notice = await screen.findByRole('alert')
  expect(notice).toHaveTextContent('This merge field won’t be filled in')
  expect(notice).toHaveTextContent('{{firstname}}')

  await submitStep()
  // Give a would-be request every chance to go out.
  await new Promise((resolve) => setTimeout(resolve, 50))
  expect(putRequests()).toHaveLength(0)
  expect(onDone).not.toHaveBeenCalled()

  // Fixing it clears the notice and the save goes through.
  replaceText(await screen.findByRole('textbox', { name: 'Body' }), 'Hi {{first_name}}')
  expect(screen.queryByText(/won’t be filled in/)).not.toBeInTheDocument()
  await submitStep()
  await waitFor(() => expect(putRequests()).toHaveLength(1))
  expect(putRequests()[0]?.body).toMatchObject({ body_text: 'Hi {{first_name}}', body_html: '' })
})

test('custom fields resolve against the live definitions: archived keys are unknown', async () => {
  customFieldsResponder = () => jsonResponse([customField('industry'), customField('legacy', true)])
  renderWithProviders(
    <StepForm
      campaignId="c-1"
      step={makeStep({ body_text: '{{custom.industry}} and {{custom.legacy}}' })}
      isFirstStep
      onDone={vi.fn()}
      onCancel={vi.fn()}
    />,
  )
  const notice = await screen.findByRole('alert')
  expect(notice).toHaveTextContent('{{custom.legacy}}')
  expect(notice).not.toHaveTextContent('{{custom.industry}}')
})

test('when custom fields can’t be loaded, custom tokens are unverified — shown, not blocking', async () => {
  customFieldsResponder = () => jsonResponse({ error: 'boom' }, 500)
  renderWithProviders(
    <StepForm
      campaignId="c-1"
      step={makeStep({ body_text: 'Your {{custom.industry}} team' })}
      isFirstStep
      onDone={vi.fn()}
      onCancel={vi.fn()}
    />,
  )
  const body = await screen.findByRole('textbox', { name: 'Body' })
  expect(await within(body).findByLabelText(/Merge field \(not checked yet\)/)).toBeInTheDocument()
  expect(screen.queryByText(/won’t be filled in/)).not.toBeInTheDocument()

  await submitStep()
  await waitFor(() => expect(putRequests()).toHaveLength(1))
})

test('saving an untouched step sends its stored copy back unchanged', async () => {
  const html = '<p>Hi <strong>{{first_name}}</strong></p>'
  renderWithProviders(
    <StepForm
      campaignId="c-1"
      step={makeStep({ subject: '{Hi|Hey} {{first_name}}', body_text: 'Hi {{first_name}}', body_html: html })}
      isFirstStep
      onDone={vi.fn()}
      onCancel={vi.fn()}
    />,
  )
  await screen.findByRole('textbox', { name: 'Body' })
  await submitStep()
  await waitFor(() => expect(putRequests()).toHaveLength(1))
  expect(putRequests()[0]?.body).toEqual({
    delay_seconds: 0,
    subject: '{Hi|Hey} {{first_name}}',
    body_text: 'Hi {{first_name}}',
    body_html: html,
  })
})

test('the first step still requires a subject; a follow-up may leave it blank', async () => {
  const { unmount } = renderWithProviders(
    <StepForm campaignId="c-1" step={makeStep()} isFirstStep onDone={vi.fn()} onCancel={vi.fn()} />,
  )
  replaceText(await screen.findByRole('textbox', { name: 'Subject' }), '')
  await submitStep()
  expect(await screen.findByText('Required')).toBeInTheDocument()
  expect(screen.getByRole('textbox', { name: 'Subject' })).toHaveAttribute('aria-invalid', 'true')
  expect(putRequests()).toHaveLength(0)
  unmount()

  renderWithProviders(
    <StepForm campaignId="c-1" step={makeStep()} isFirstStep={false} onDone={vi.fn()} onCancel={vi.fn()} />,
  )
  replaceText(await screen.findByRole('textbox', { name: 'Subject' }), '')
  await submitStep()
  await waitFor(() => expect(putRequests()).toHaveLength(1))
  expect(putRequests()[0]?.body).toMatchObject({ subject: '' })
})
