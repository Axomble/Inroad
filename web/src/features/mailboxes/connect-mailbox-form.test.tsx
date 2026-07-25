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
let requests: CapturedRequest[]

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: jsonHeaders })
}

beforeEach(() => {
  requests = []
  connectResponder = () =>
    jsonResponse({ id: 'm-1', email: 'sender@company.com', provider: 'smtp', status: 'active' })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      // connectMailbox is a mutation → RTK Query hands fetch a `Request` object.
      const req = input as Request
      const body = req.body ? await req.clone().json() : undefined
      requests.push({ method: req.method.toUpperCase(), url: req.url, body })
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
    use_tls: true,
  })
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
