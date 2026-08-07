import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { ReplyComposer } from '../reply-composer'

const jsonHeaders = { 'content-type': 'application/json' }
const THREAD_ID = 'thread-1'

let replyResponder: () => Response
let lastRequestBody: unknown

beforeEach(() => {
  replyResponder = () => new Response(null, { status: 202 })
  lastRequestBody = undefined

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      lastRequestBody = input instanceof Request
        ? await input.clone().json()
        : typeof init?.body === 'string'
          ? JSON.parse(init.body)
          : undefined
      return replyResponder()
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('the send button is disabled with an empty or whitespace-only textarea', () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  const send = screen.getByRole('button', { name: /send/i })
  expect(send).toBeDisabled()

  fireEvent.change(screen.getByLabelText(/^reply$/i), { target: { value: '   ' } })
  expect(send).toBeDisabled()
})

test('sending a reply posts the body, clears the textarea, and shows a success status', async () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  const textarea = screen.getByLabelText(/^reply$/i)
  fireEvent.change(textarea, { target: { value: 'Thanks for getting back to me!' } })
  fireEvent.click(screen.getByRole('button', { name: /send/i }))

  await waitFor(() => expect(lastRequestBody).toEqual({ body_text: 'Thanks for getting back to me!' }))
  expect(await screen.findByRole('status')).toHaveTextContent(/reply sent — it will appear in the thread shortly/i)
  expect(textarea).toHaveValue('')
})

test('a 409 shows the suppressed/no-inbound-message message, not a generic failure', async () => {
  replyResponder = () => new Response(JSON.stringify({ error: 'recipient suppressed' }), { status: 409, headers: jsonHeaders })
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.change(screen.getByLabelText(/^reply$/i), { target: { value: 'hello there' } })
  fireEvent.click(screen.getByRole('button', { name: /send/i }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/unsubscribed or suppressed — the reply was not sent/i)
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
})

test('a generic failure shows the fallback message', async () => {
  replyResponder = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.change(screen.getByLabelText(/^reply$/i), { target: { value: 'hello there' } })
  fireEvent.click(screen.getByRole('button', { name: /send/i }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/couldn't send the reply/i)
})

test('is replaced with an explanation, not an interactive composer, when the thread has no inbound message', () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage={false} />)

  expect(screen.queryByRole('button', { name: /send/i })).not.toBeInTheDocument()
  expect(screen.queryByLabelText(/^reply$/i)).not.toBeInTheDocument()
  expect(screen.getByText(/you can reply once this contact has sent an inbound message/i)).toBeInTheDocument()
})
