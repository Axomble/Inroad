import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { api } from '@/store/api'
import { ReplyComposer } from '../reply-composer'

// The composer only needs a router for the AI-settings link (same stub as
// features/agent/composer.test.tsx).
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, ...props }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

const jsonHeaders = { 'content-type': 'application/json' }
const THREAD_ID = 'thread-1'

function json(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { ...jsonHeaders, ...headers } })
}

let replyResponder: () => Response
let draftResponder: () => Response | Promise<Response>
let modelsResponder: () => Response
let lastRequestBody: unknown
let lastRequestPath: string
let sendCalls: number
let draftCalls: number

beforeEach(() => {
  replyResponder = () => new Response(null, { status: 202 })
  draftResponder = () => json({ body_text: 'Thursday works — how about 10am?' })
  modelsResponder = () => json({ models: [{ id: 'anthropic/claude', label: 'Claude', enabled: true }] })
  lastRequestBody = undefined
  lastRequestPath = ''
  sendCalls = 0
  draftCalls = 0

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const { pathname } = new URL(href, 'http://localhost')

      if (pathname.endsWith('/ai/models')) return modelsResponder()

      lastRequestPath = pathname
      lastRequestBody = isRequest
        ? await input.clone().json().catch(() => undefined)
        : typeof init?.body === 'string'
          ? JSON.parse(init.body)
          : undefined

      if (pathname.endsWith('/draft-reply')) {
        draftCalls += 1
        return draftResponder()
      }
      sendCalls += 1
      return replyResponder()
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  // Defensive: a failing assertion inside a fake-timers test must not leak
  // fake time into the next test in the file.
  vi.useRealTimers()
})

const draftButton = () => screen.getByRole('button', { name: /draft a reply/i })
const sendButton = () => screen.getByRole('button', { name: /^send$/i })
const textarea = () => screen.getByLabelText(/^reply$/i)

test('the send button is disabled with an empty or whitespace-only textarea', () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  expect(sendButton()).toBeDisabled()

  fireEvent.change(textarea(), { target: { value: '   ' } })
  expect(sendButton()).toBeDisabled()
})

// Send now goes through the SCHEDULE endpoint, so the reply waits out the
// workspace's undo window and stays cancellable. The success copy says "queued"
// rather than "sent" because that is now the truth — the mail has not left yet.
test('sending a reply schedules it, clears the textarea, and shows a queued status', async () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.change(textarea(), { target: { value: 'Thanks for getting back to me!' } })
  fireEvent.click(sendButton())

  await waitFor(() => expect(lastRequestBody).toEqual({ body_text: 'Thanks for getting back to me!' }))
  expect(await screen.findByRole('status')).toHaveTextContent(/reply queued — you can still undo it/i)
  expect(textarea()).toHaveValue('')
  expect(lastRequestPath).toContain('/schedule-reply')
})

// Send later attaches an explicit instant. Without send_at the server applies
// the undo window; with it, the reply waits for the chosen moment.
test('send later posts the chosen instant as send_at', async () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)
  fireEvent.change(textarea(), { target: { value: 'Next week please' } })

  fireEvent.click(screen.getByRole('button', { name: /send later/i }))
  const at = new Date(Date.now() + 3 * 86_400_000)
  const pad = (n: number) => String(n).padStart(2, '0')
  const value = `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T09:00`

  fireEvent.change(screen.getByLabelText(/specific date and time/i), { target: { value } })
  fireEvent.click(screen.getByRole('button', { name: /^schedule$/i }))

  await waitFor(() => {
    const body = lastRequestBody as { body_text: string; send_at?: string }
    expect(body.body_text).toBe('Next week please')
    expect(body.send_at).toBeTruthy()
  })
})

test('a send-later instant in the past is refused inline, without a request', async () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)
  fireEvent.change(textarea(), { target: { value: 'oops' } })

  fireEvent.click(screen.getByRole('button', { name: /send later/i }))
  fireEvent.change(screen.getByLabelText(/specific date and time/i), { target: { value: '2020-01-01T09:00' } })
  fireEvent.click(screen.getByRole('button', { name: /^schedule$/i }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/future/i)
  expect(sendCalls).toBe(0)
})

test('a 409 shows the suppressed/no-inbound-message message, not a generic failure', async () => {
  replyResponder = () => json({ error: 'recipient suppressed' }, 409)
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.change(textarea(), { target: { value: 'hello there' } })
  fireEvent.click(sendButton())

  expect(await screen.findByRole('alert')).toHaveTextContent(/unsubscribed or suppressed — the reply was not sent/i)
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
})

test('a generic failure shows the fallback message', async () => {
  replyResponder = () => json({ error: 'boom' }, 500)
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.change(textarea(), { target: { value: 'hello there' } })
  fireEvent.click(sendButton())

  expect(await screen.findByRole('alert')).toHaveTextContent(/couldn't send the reply/i)
})

test('is replaced with an explanation, not an interactive composer, when the thread has no inbound message', () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage={false} />)

  expect(screen.queryByRole('button', { name: /^send$/i })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /draft a reply/i })).not.toBeInTheDocument()
  expect(screen.queryByLabelText(/^reply$/i)).not.toBeInTheDocument()
  expect(screen.getByText(/you can reply once this contact has sent an inbound message/i)).toBeInTheDocument()
})

// MAX_BODY_LENGTH boundary: the component's own check is `length >
// MAX_BODY_LENGTH`, so exactly 100,000 characters is still allowed and only
// 100,001 crosses into "too long".
test('exactly 100,000 characters is allowed; 100,001 shows the too-long alert and disables Send', () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.change(textarea(), { target: { value: 'a'.repeat(100_000) } })
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(sendButton()).toBeEnabled()

  fireEvent.change(textarea(), { target: { value: 'a'.repeat(100_001) } })
  expect(screen.getByRole('alert')).toHaveTextContent(/too long — max 100,000 characters \(100,001 so far\)/i)
  expect(sendButton()).toBeDisabled()
})

// The 202 only means "queued" — DELAYED_REFETCH_MS covers the gap before the
// worker has plausibly delivered the outbound message with one extra,
// bounded cache-tag invalidation.
test('a successful send schedules a delayed InboxThread invalidation that fires after 2s', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const invalidateSpy = vi.spyOn(api.util, 'invalidateTags')

  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)
  fireEvent.change(textarea(), { target: { value: 'hi there' } })
  fireEvent.click(sendButton())

  await screen.findByRole('status')
  // The mutation's own `invalidatesTags` already fires immediately on
  // success — clear that call so the assertion below is specifically about
  // the delayed one.
  invalidateSpy.mockClear()

  await vi.advanceTimersByTimeAsync(2000)

  expect(invalidateSpy).toHaveBeenCalledWith([{ type: 'InboxThread', id: THREAD_ID }])
})

test('unmounting before the 2s delay elapses cancels the delayed invalidation', async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  const invalidateSpy = vi.spyOn(api.util, 'invalidateTags')

  const { unmount } = renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)
  fireEvent.change(textarea(), { target: { value: 'hi there' } })
  fireEvent.click(sendButton())

  await screen.findByRole('status')
  invalidateSpy.mockClear()
  unmount()

  await vi.advanceTimersByTimeAsync(2000)

  expect(invalidateSpy).not.toHaveBeenCalled()
})

// ——— AI draft ———

test('drafting into an empty textarea fills it without a confirmation prompt, and never sends', async () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.click(draftButton())

  await waitFor(() => expect(textarea()).toHaveValue('Thursday works — how about 10am?'))
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(sendCalls).toBe(0)
  expect(await screen.findByRole('status')).toHaveTextContent(/draft ready — review and edit it before sending/i)
})

test('a landed draft focuses the textarea with the caret at the end', async () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.click(draftButton())

  await waitFor(() => expect(textarea()).toHaveFocus())
  const el = textarea() as HTMLTextAreaElement
  expect(el.selectionStart).toBe(el.value.length)
  expect(el.selectionEnd).toBe(el.value.length)
})

test('drafting over existing text asks first, and confirming replaces it', async () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.change(textarea(), { target: { value: 'my own words' } })
  fireEvent.click(draftButton())

  expect(await screen.findByText(/replace what you've written\?/i)).toBeInTheDocument()
  expect(draftCalls).toBe(0)

  fireEvent.click(screen.getByRole('button', { name: /replace with a draft/i }))

  await waitFor(() => expect(textarea()).toHaveValue('Thursday works — how about 10am?'))
  expect(draftCalls).toBe(1)
})

test('cancelling the overwrite prompt leaves the text alone and fires no request', async () => {
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.change(textarea(), { target: { value: 'my own words' } })
  fireEvent.click(draftButton())
  fireEvent.click(await screen.findByRole('button', { name: /keep my text/i }))

  await waitFor(() => expect(screen.queryByText(/replace what you've written\?/i)).not.toBeInTheDocument())
  expect(textarea()).toHaveValue('my own words')
  expect(draftCalls).toBe(0)
  expect(sendCalls).toBe(0)
})

test('Send and the draft button are both disabled while a draft is generating', async () => {
  let release: (r: Response) => void = () => {}
  draftResponder = () => new Promise<Response>((resolve) => (release = resolve))

  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)
  fireEvent.change(textarea(), { target: { value: 'my own words' } })
  fireEvent.click(draftButton())
  fireEvent.click(await screen.findByRole('button', { name: /replace with a draft/i }))

  await waitFor(() => expect(draftButton()).toBeDisabled())
  expect(draftButton()).toHaveAttribute('aria-busy', 'true')
  expect(sendButton()).toBeDisabled()

  release(json({ body_text: 'ok' }))
  await waitFor(() => expect(textarea()).toHaveValue('ok'))
  expect(sendButton()).toBeEnabled()
})

test('a "no AI model configured" failure (422) points at Settings → AI instead of offering a retry', async () => {
  draftResponder = () => json({ error: 'no AI model is configured for this workspace' }, 422)
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.click(draftButton())

  expect(await screen.findByRole('alert')).toHaveTextContent(/no ai model is configured for this workspace/i)
  expect(screen.getByRole('link', { name: /set one up in settings → ai/i })).toHaveAttribute(
    'href',
    '/app/settings/ai',
  )
})

test('a 429 reads as "wait and retry", not as a failure that lost the user work', async () => {
  draftResponder = () => json({ error: 'rate limited' }, 429, { 'retry-after': '30' })
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.click(draftButton())

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/rate limited — wait 30 seconds and try again\. nothing was lost/i)
  expect(alert).not.toHaveTextContent(/couldn't draft/i)
  // The draft button comes back so the user can retry after waiting.
  expect(draftButton()).toBeEnabled()
})

test('a 409 while drafting says there is nothing to reply to yet', async () => {
  draftResponder = () => json({ error: 'no inbound message' }, 409)
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.click(draftButton())

  expect(await screen.findByRole('alert')).toHaveTextContent(/no inbound message to draft a reply to yet/i)
})

test('a provider failure (502) keeps the user unblocked rather than blaming them', async () => {
  draftResponder = () => json({ error: 'ai provider request failed' }, 502)
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.click(draftButton())

  expect(await screen.findByRole('alert')).toHaveTextContent(
    /the ai provider call failed.*write the reply yourself/i,
  )
})

test('a provider timeout (504) says a retry may work', async () => {
  draftResponder = () => json({ error: 'ai provider timed out' }, 504)
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.click(draftButton())

  expect(await screen.findByRole('alert')).toHaveTextContent(
    /did not respond in time.*trying again may work/i,
  )
  expect(draftButton()).toBeEnabled()
})

test('a failed draft leaves the existing text untouched', async () => {
  draftResponder = () => json({ error: 'rate limited' }, 429)
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  fireEvent.change(textarea(), { target: { value: 'my own words' } })
  fireEvent.click(draftButton())
  fireEvent.click(await screen.findByRole('button', { name: /replace with a draft/i }))

  await screen.findByRole('alert')
  expect(textarea()).toHaveValue('my own words')
})

test('the draft button is disabled with a pointer to AI settings when no model is enabled', async () => {
  modelsResponder = () => json({ models: [{ id: 'anthropic/claude', label: 'Claude', enabled: false }] })
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  await waitFor(() => expect(draftButton()).toBeDisabled())
  expect(screen.getByRole('link', { name: /configure one in settings → ai/i })).toHaveAttribute(
    'href',
    '/app/settings/ai',
  )
  // Sending is a human action that needs no model — it must stay available.
  fireEvent.change(textarea(), { target: { value: 'my own words' } })
  expect(sendButton()).toBeEnabled()
})

test('an unreadable model list leaves drafting available rather than locking the feature out', async () => {
  modelsResponder = () => json({ error: 'boom' }, 500)
  renderWithProviders(<ReplyComposer threadId={THREAD_ID} hasInboundMessage />)

  await waitFor(() => expect(screen.queryByText(/drafting needs an ai model/i)).not.toBeInTheDocument())
  expect(draftButton()).toBeEnabled()
})
