import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { ConnectMailboxForm } from './connect-mailbox-form'

// The SMTP/IMAP connect form is the manual mailbox on-ramp. These lock: a valid
// submit posts the entered fields and calls onDone; the 422 connection-test and
// 409 duplicate paths surface the exact typed-error copy; and client validation
// (missing host / bad port) blocks the mutation entirely.

const jsonHeaders = { 'content-type': 'application/json' }

type CapturedRequest = { method: string; url: string; body: unknown }

let connectResponder: () => Response
let authMeResponder: () => Response
let warmupResponder: () => Response
let requests: CapturedRequest[]

/** The warmup enable the form fires for the mailbox it just created. */
function warmupRequest() {
  return requests.find((r) => r.method === 'PUT' && r.url.endsWith('/mailboxes/m-1/warmup'))
}

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

/**
 * A signed-in session, so the form's `useEmailVerified` actually queries
 * `/auth/me` instead of skipping (the default `idle` status means "bootstrap
 * hasn't run", where nothing is gated).
 */
const AUTHED = { auth: { status: 'authed' as const, accessToken: 'token' } }

beforeEach(() => {
  requests = []
  connectResponder = () =>
    jsonResponse({ id: 'm-1', email: 'sender@company.com', provider: 'smtp', status: 'active' })
  authMeResponder = () => jsonResponse({ user_id: 'u-1', email: 'me@company.com', email_verified: true })
  warmupResponder = () => jsonResponse({ mailbox_id: 'm-1', enabled: true })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      // connectMailbox is a mutation → RTK Query hands fetch a `Request` object.
      const req = input as Request
      const body = req.body ? await req.clone().json() : undefined
      requests.push({ method: req.method.toUpperCase(), url: req.url, body })
      if (req.url.includes('/auth/me')) return authMeResponder()
      if (req.url.endsWith('/warmup')) return warmupResponder()
      return connectResponder()
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** Fills the four required fields (ports have valid defaults of 587/993). */
function fillRequiredFields() {
  fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'sender@company.com' } })
  fireEvent.change(screen.getByLabelText('SMTP host'), { target: { value: 'smtp.company.com' } })
  fireEvent.change(screen.getByLabelText('IMAP host'), { target: { value: 'imap.company.com' } })
  fireEvent.change(screen.getByLabelText(/Password/), { target: { value: 'app-password-123' } })
}

function submit() {
  fireEvent.click(screen.getByRole('button', { name: /^connect mailbox$/i }))
}

test('a valid submit posts the entered fields and calls onDone on success', async () => {
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />)

  fillRequiredFields()
  submit()

  await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1))

  const posted = requests.find((r) => r.method === 'POST' && r.url.endsWith('/mailboxes'))
  expect(posted).toBeDefined()
  expect(posted?.body).toMatchObject({
    email: 'sender@company.com',
    smtp_host: 'smtp.company.com',
    smtp_port: 587,
    imap_host: 'imap.company.com',
    imap_port: 993,
    secret: 'app-password-123',
    allow_plaintext: false,
  })
})

test('checking "Allow plaintext" submits allow_plaintext: true', async () => {
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />)

  fillRequiredFields()
  fireEvent.click(screen.getByLabelText(/Allow plaintext/))
  submit()

  await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1))

  const posted = requests.find((r) => r.method === 'POST' && r.url.endsWith('/mailboxes'))
  expect(posted?.body).toMatchObject({ allow_plaintext: true })
})

test('a 422 surfaces the "connection test failed" copy and does not call onDone', async () => {
  connectResponder = () => jsonResponse({ error: 'connection test failed' }, 422)
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />)

  fillRequiredFields()
  submit()

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/Connection test failed — check host, port, and credentials\./)
  expect(onDone).not.toHaveBeenCalled()
})

test('a 409 surfaces the duplicate-mailbox copy and does not call onDone', async () => {
  connectResponder = () => jsonResponse({ error: 'already connected' }, 409)
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />)

  fillRequiredFields()
  submit()

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/A mailbox with this email is already connected\./)
  expect(onDone).not.toHaveBeenCalled()
})

// Warmup-on-connect: warming is the default-correct action for a new mailbox,
// so the checkbox ships checked and the enable rides along with the connect.
// What must not happen: a warmup hiccup making a successful connect look failed.

test('warming is on by default', async () => {
  renderWithProviders(<ConnectMailboxForm onDone={vi.fn()} onCancel={() => {}} />)

  expect(await screen.findByLabelText(/start warming this mailbox/i)).toBeChecked()
})

test('connecting with warming on posts the mailbox, then enables warmup with default settings', async () => {
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />)

  fillRequiredFields()
  submit()

  await waitFor(() => expect(onDone).toHaveBeenCalledWith({ warmupFailed: false }))

  const posted = requests.find((r) => r.method === 'POST' && r.url.endsWith('/mailboxes'))
  // The checkbox is a form field, not part of the connect contract.
  expect(posted?.body).not.toHaveProperty('start_warmup')
  // Empty settings = the server's own default ramp; no config UI needed here.
  expect(warmupRequest()?.body).toEqual({})
})

test('unchecking it connects the mailbox and leaves warmup alone', async () => {
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />)

  fillRequiredFields()
  fireEvent.click(screen.getByLabelText(/start warming this mailbox/i))
  submit()

  await waitFor(() => expect(onDone).toHaveBeenCalledWith({ warmupFailed: false }))
  expect(warmupRequest()).toBeUndefined()
})

test('a failed warmup enable still reports the mailbox as connected', async () => {
  warmupResponder = () => jsonResponse({ error: 'warmup unavailable' }, 500)
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />)

  fillRequiredFields()
  submit()

  // Connected, with warmup flagged — NOT a failed connect: the mailbox exists,
  // so the form still closes and the page explains the one part that didn't.
  await waitFor(() => expect(onDone).toHaveBeenCalledWith({ warmupFailed: true }))
  expect(warmupRequest()).toBeDefined()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

// Email verification: POST /mailboxes sits behind `auth.RequireVerified`, so
// the form both gates the control up front and has to keep the 403 legible —
// the gate is a UX affordance, the server check is the authority.

test('an unverified account disables the submit, explains why, and never posts', async () => {
  authMeResponder = () => jsonResponse({ user_id: 'u-1', email: 'me@company.com', email_verified: false })
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />, {
    preloadedState: AUTHED,
  })

  // Re-queried each attempt: the gated button is a different element than the
  // plain one it replaces, so a captured node would go stale.
  await waitFor(() =>
    expect(screen.getByRole('button', { name: /^connect mailbox$/i })).toHaveAttribute(
      'aria-disabled',
      'true',
    ),
  )

  // The reason is programmatically associated, not hover-only: a keyboard or
  // screen-reader user gets it without a pointer.
  const button = screen.getByRole('button', { name: /^connect mailbox$/i })
  const hintId = button.getAttribute('aria-describedby')
  expect(document.getElementById(hintId ?? '')).toHaveTextContent(
    /Verify your email address to connect a mailbox\./,
  )

  fillRequiredFields()
  submit()

  await waitFor(() => expect(requests.some((r) => r.url.includes('/auth/me'))).toBe(true))
  expect(requests.some((r) => r.method === 'POST' && r.url.endsWith('/mailboxes'))).toBe(false)
  expect(onDone).not.toHaveBeenCalled()
})

test('a verified account leaves the submit enabled and posts', async () => {
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />, {
    preloadedState: AUTHED,
  })

  await waitFor(() => expect(requests.some((r) => r.url.includes('/auth/me'))).toBe(true))
  const button = screen.getByRole('button', { name: /^connect mailbox$/i })
  expect(button).not.toHaveAttribute('aria-disabled')

  fillRequiredFields()
  submit()

  await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1))
})

test('a 403 email_not_verified surfaces the verification copy, not the generic failure', async () => {
  connectResponder = () => jsonResponse({ error: 'email_not_verified' }, 403)
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />)

  fillRequiredFields()
  submit()

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/Verify your email address to connect a mailbox\./)
  expect(alert).not.toHaveTextContent(/Couldn’t connect the mailbox/)
  expect(onDone).not.toHaveBeenCalled()
})

test('a missing SMTP host blocks the mutation and shows a field error', async () => {
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />)

  // Everything valid except the SMTP host, which stays empty.
  fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'sender@company.com' } })
  fireEvent.change(screen.getByLabelText('IMAP host'), { target: { value: 'imap.company.com' } })
  fireEvent.change(screen.getByLabelText(/Password/), { target: { value: 'app-password-123' } })
  submit()

  expect(await screen.findByText('Required')).toBeInTheDocument()
  expect(requests.some((r) => r.method === 'POST')).toBe(false)
  expect(onDone).not.toHaveBeenCalled()
})

test('an out-of-range port blocks the mutation with the port error', async () => {
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />)

  fillRequiredFields()
  fireEvent.change(screen.getByLabelText('SMTP port'), { target: { value: '70000' } })
  submit()

  expect(await screen.findByText('Port must be between 1 and 65535')).toBeInTheDocument()
  expect(requests.some((r) => r.method === 'POST')).toBe(false)
  expect(onDone).not.toHaveBeenCalled()
})

test('the submit button shows the in-flight "Testing connection…" label', async () => {
  // Hold the response open so the loading state is observable.
  let release: () => void = () => {}
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const req = input as Request
      requests.push({ method: req.method.toUpperCase(), url: req.url, body: undefined })
      await gate
      return jsonResponse({ id: 'm-1', status: 'active' })
    }),
  )
  const onDone = vi.fn()
  renderWithProviders(<ConnectMailboxForm onDone={onDone} onCancel={() => {}} />)

  fillRequiredFields()
  submit()

  expect(await screen.findByRole('button', { name: /Testing connection…/ })).toBeDisabled()
  release()
  await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1))
})
