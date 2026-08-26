import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { ComposeWindow } from '../compose-window'

// SortMenu (the From picker) is a Radix DropdownMenu; polyfill what it touches.
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
  // jsdom has no crypto.randomUUID in every version; the window mints a draft id
  // with it.
  const cryptoObj = globalThis.crypto as unknown as Record<string, unknown>
  cryptoObj.randomUUID ??= () => '11111111-2222-3333-4444-555555555555'
})

const jsonHeaders = { 'content-type': 'application/json' }

interface Call {
  method: string
  path: string
  body: string
}

let calls: Call[]
let sendStatus: number
let sendDetail: string
let onClose: () => void
let closed: boolean

const MAILBOX_ID = 'mb-1'

beforeEach(() => {
  calls = []
  sendStatus = 201
  sendDetail = ''
  closed = false
  onClose = () => {
    closed = true
  }

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const url = new URL(href, 'http://localhost')
      const method = (isRequest ? input.method : (init?.method ?? 'GET')).toUpperCase()
      const body = isRequest ? await input.clone().text() : typeof init?.body === 'string' ? init.body : ''
      calls.push({ method, path: url.pathname, body })

      if (url.pathname.endsWith('/mailboxes')) {
        return new Response(JSON.stringify([{ id: MAILBOX_ID, email: 'sales@acme.test' }]), {
          status: 200,
          headers: jsonHeaders,
        })
      }
      if (url.pathname.includes('/inbox/drafts')) {
        if (method === 'DELETE') return new Response(null, { status: 204 })
        return new Response(
          JSON.stringify({
            id: '11111111-2222-3333-4444-555555555555',
            mailbox_id: null,
            to_emails: [],
            cc_emails: [],
            bcc_emails: [],
            subject: '',
            body_text: '',
            updated_at: new Date().toISOString(),
          }),
          { status: 200, headers: jsonHeaders },
        )
      }
      if (url.pathname.endsWith('/inbox/composes')) {
        if (sendStatus !== 201) {
          return new Response(JSON.stringify({ error: sendDetail || 'nope' }), {
            status: sendStatus,
            headers: jsonHeaders,
          })
        }
        return new Response(JSON.stringify({ id: 'pc-1' }), { status: 201, headers: jsonHeaders })
      }
      return new Response(JSON.stringify({ error: 'unhandled' }), { status: 404, headers: jsonHeaders })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

const toField = () => screen.getByLabelText('To')
const bodyField = () => screen.getByPlaceholderText('Write your message…')
const sendButton = () => screen.getByRole('button', { name: /^send$/i })

function composeCalls(): Call[] {
  return calls.filter((c) => c.path.endsWith('/inbox/composes'))
}

/** Fills the minimum a send needs. */
async function fillValid() {
  await waitFor(() => expect(screen.getByRole('button', { name: /sales@acme\.test/ })).toBeInTheDocument())
  fireEvent.change(toField(), { target: { value: 'ada@prospect.test,' } })
  fireEvent.change(bodyField(), { target: { value: 'Hello there' } })
}

test('a single connected mailbox is chosen automatically', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  // No decision to make, so the operator is not asked to make one.
  expect(await screen.findByRole('button', { name: /sales@acme\.test/ })).toBeInTheDocument()
})

test('typing a comma commits a recipient chip', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  fireEvent.change(toField(), { target: { value: 'ada@prospect.test,' } })

  expect(await screen.findByRole('button', { name: /remove ada@prospect\.test/i })).toBeInTheDocument()
})

test('a chip can be removed', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  fireEvent.change(toField(), { target: { value: 'ada@prospect.test,' } })
  fireEvent.click(await screen.findByRole('button', { name: /remove ada@prospect\.test/i }))

  expect(screen.queryByRole('button', { name: /remove ada@prospect\.test/i })).not.toBeInTheDocument()
})

test('send is blocked until there is a recipient and a body', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  await waitFor(() => expect(screen.getByRole('button', { name: /sales@acme\.test/ })).toBeInTheDocument())

  expect(sendButton()).toBeDisabled()
  fireEvent.change(toField(), { target: { value: 'ada@prospect.test,' } })
  expect(sendButton()).toBeDisabled()
  fireEvent.change(bodyField(), { target: { value: 'Hello' } })
  expect(sendButton()).toBeEnabled()
})

test('sending posts the message and closes the window', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  await fillValid()
  fireEvent.click(sendButton())

  await waitFor(() => expect(composeCalls()).toHaveLength(1))
  const sent = JSON.parse(composeCalls()[0]?.body ?? '{}') as {
    mailbox_id: string
    to_emails: string[]
    body_text: string
    draft_id: string
  }
  expect(sent.mailbox_id).toBe(MAILBOX_ID)
  expect(sent.to_emails).toEqual(['ada@prospect.test'])
  expect(sent.body_text).toBe('Hello there')
  // The draft id travels with the send so the server can discard it.
  expect(sent.draft_id).toBeTruthy()

  await waitFor(() => expect(closed).toBe(true))
})

// A failed send must leave the window open — the operator's text is in it.
test('a failed send keeps the window open with the text intact', async () => {
  sendStatus = 500
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  await fillValid()
  fireEvent.click(sendButton())

  expect(await screen.findByRole('alert')).toHaveTextContent(/couldn't send/i)
  expect(closed).toBe(false)
  expect(bodyField()).toHaveValue('Hello there')
})

// 422 carries the server's own explanation of WHICH rule broke; that detail is
// worth showing rather than replacing with generic copy.
test('a 422 surfaces the server explanation', async () => {
  sendStatus = 422
  sendDetail = 'inbox: at most 25 recipients per message'
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  await fillValid()
  fireEvent.click(sendButton())

  expect(await screen.findByRole('alert')).toHaveTextContent(/25 recipients/)
})

test('a suppressed recipient is explained rather than shown as a status', async () => {
  sendStatus = 409
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  await fillValid()
  fireEvent.click(sendButton())

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/unsubscribed or bounced/i)
  expect(alert).not.toHaveTextContent('409')
})

// A malformed address is flagged on the chip AND blocks the send, rather than
// being accepted and failing server-side.
test('an invalid address blocks sending and is called out', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  await waitFor(() => expect(screen.getByRole('button', { name: /sales@acme\.test/ })).toBeInTheDocument())
  fireEvent.change(toField(), { target: { value: 'not-an-email,' } })
  fireEvent.change(bodyField(), { target: { value: 'Hello' } })

  expect(await screen.findByRole('alert')).toHaveTextContent(/doesn't look like an email/i)
  expect(sendButton()).toBeDisabled()
  expect(composeCalls()).toHaveLength(0)
})

// Each recipient gets their own copy, so this needs saying BEFORE Send rather
// than after someone asks why nobody could reply-all.
test('multiple recipients are warned they get separate copies', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  fireEvent.change(toField(), { target: { value: 'a@x.test,b@x.test,' } })

  expect(await screen.findByText(/own copy/i)).toBeInTheDocument()
})

test('Cc and Bcc are hidden until asked for', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  expect(screen.queryByLabelText('Cc')).not.toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /add cc \/ bcc/i }))

  expect(await screen.findByLabelText('Cc')).toBeInTheDocument()
  expect(screen.getByLabelText('Bcc')).toBeInTheDocument()
})

// Autosave waits for a pause rather than firing per keystroke. Real timers with
// a waitFor, not fake ones: RTK Query's own dispatch/serialization is async, so
// advancing a fake clock fires the debounce without letting the resulting
// request actually reach the stubbed fetch.
test('typing autosaves a draft after a pause', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  fireEvent.change(bodyField(), { target: { value: 'half a thought' } })

  const drafts = () => calls.filter((c) => c.path.includes('/inbox/drafts') && c.method === 'PUT')
  // Nothing yet: the debounce has not elapsed.
  expect(drafts()).toHaveLength(0)

  await waitFor(() => expect(drafts().length).toBeGreaterThan(0), { timeout: 4000 })
  expect(JSON.parse(drafts()[0]?.body ?? '{}')).toMatchObject({ body_text: 'half a thought' })
})

test('an existing draft is reopened with its content', () => {
  renderWithProviders(
    <ComposeWindow
      onClose={onClose}
      resumeDraft={{
        id: 'd-1',
        mailbox_id: MAILBOX_ID,
        to_emails: ['ada@prospect.test'],
        cc_emails: ['grace@prospect.test'],
        bcc_emails: [],
        subject: 'Picking this back up',
        body_text: 'where I left off',
        updated_at: new Date().toISOString(),
      }}
    />,
  )

  expect(screen.getByDisplayValue('Picking this back up')).toBeInTheDocument()
  expect(screen.getByDisplayValue('where I left off')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /remove ada@prospect\.test/i })).toBeInTheDocument()
  // Cc was populated, so the field is already open rather than hidden behind
  // "Add Cc / Bcc" where its content would be invisible.
  expect(screen.getByLabelText('Cc')).toBeInTheDocument()
})

test('discarding deletes the draft and closes', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  fireEvent.change(bodyField(), { target: { value: 'never mind' } })

  fireEvent.click(screen.getByRole('button', { name: /discard draft/i }))

  await waitFor(() => {
    expect(calls.some((c) => c.method === 'DELETE' && c.path.includes('/inbox/drafts'))).toBe(true)
  })
  expect(closed).toBe(true)
})

test('minimizing keeps the window reachable rather than losing it', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  fireEvent.change(screen.getByLabelText(/^subj/i), { target: { value: 'Half-written' } })

  fireEvent.click(screen.getByRole('button', { name: /minimize compose/i }))
  expect(screen.queryByPlaceholderText('Write your message…')).not.toBeInTheDocument()

  // The collapsed bar names the message, so it can be found again.
  fireEvent.click(screen.getByRole('button', { name: 'Half-written' }))
  expect(await screen.findByPlaceholderText('Write your message…')).toBeInTheDocument()
})

test('send later posts an explicit send_at', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  await fillValid()

  fireEvent.click(screen.getByRole('button', { name: /send later/i }))
  const at = new Date(Date.now() + 2 * 86_400_000)
  const pad = (n: number) => String(n).padStart(2, '0')
  const value = `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T09:00`
  fireEvent.change(screen.getByLabelText(/specific date and time/i), { target: { value } })
  fireEvent.click(screen.getByRole('button', { name: /^schedule$/i }))

  await waitFor(() => expect(composeCalls()).toHaveLength(1))
  const sent = JSON.parse(composeCalls()[0]?.body ?? '{}') as { send_at?: string }
  expect(sent.send_at).toBeTruthy()
})

test('a send-later instant in the past is refused inline', async () => {
  renderWithProviders(<ComposeWindow onClose={onClose} />)
  await fillValid()

  fireEvent.click(screen.getByRole('button', { name: /send later/i }))
  fireEvent.change(screen.getByLabelText(/specific date and time/i), { target: { value: '2020-01-01T09:00' } })
  fireEvent.click(screen.getByRole('button', { name: /^schedule$/i }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/future/i)
  expect(composeCalls()).toHaveLength(0)
})
