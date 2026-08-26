import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { UndoSendPill } from '../undo-send-pill'
import type { InboxPendingReply } from '../api'

const jsonHeaders = { 'content-type': 'application/json' }

let cancelCalls: { method: string; path: string }[]
let cancelStatus: number

function pending(overrides: Partial<InboxPendingReply> = {}): InboxPendingReply {
  return {
    id: 'p-1',
    thread_id: 't-1',
    status: 'scheduled',
    send_after: new Date(Date.now() + 20_000).toISOString(),
    sent_at: null,
    body_text: 'on it',
    last_error: '',
    cancellable: true,
    thread_subject: 'Re: intro',
    contact_email: 'jamie@prospect.test',
    created_at: new Date().toISOString(),
    ...overrides,
  }
}

beforeEach(() => {
  cancelCalls = []
  cancelStatus = 204
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const url = new URL(href, 'http://localhost')
      const method = (isRequest ? input.method : (init?.method ?? 'GET')).toUpperCase()
      cancelCalls.push({ method, path: url.pathname })

      if (cancelStatus === 204) return new Response(null, { status: 204 })
      return new Response(JSON.stringify({ error: 'nope' }), { status: cancelStatus, headers: jsonHeaders })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

test('shows a live countdown for a reply inside the undo window', () => {
  renderWithProviders(<UndoSendPill pending={pending()} />)

  expect(screen.getByRole('status')).toHaveTextContent(/sending in \d+s/i)
  expect(screen.getByRole('button', { name: /undo/i })).toBeInTheDocument()
})

test('the countdown ticks down', async () => {
  vi.useFakeTimers()
  try {
    renderWithProviders(<UndoSendPill pending={pending({ send_after: new Date(Date.now() + 30_000).toISOString() })} />)
    const before = screen.getByRole('status').textContent

    await vi.advanceTimersByTimeAsync(3000)

    expect(screen.getByRole('status').textContent).not.toBe(before)
  } finally {
    vi.useRealTimers()
  }
})

// A far-future schedule gets no ticking timer — a four-digit countdown is
// meaningless, and the interval would run for days on an open tab.
test('a scheduled-for-later reply names the moment instead of counting down', () => {
  const at = new Date(Date.now() + 3 * 86_400_000).toISOString()
  renderWithProviders(<UndoSendPill pending={pending({ send_after: at })} />)

  const status = screen.getByRole('status')
  expect(status).toHaveTextContent(/scheduled for/i)
  expect(status).not.toHaveTextContent(/sending in/i)
})

test('clicking Undo cancels the queued reply', async () => {
  renderWithProviders(<UndoSendPill pending={pending()} />)

  fireEvent.click(screen.getByRole('button', { name: /undo/i }))

  await waitFor(() => {
    const call = cancelCalls.find((c) => c.method === 'DELETE')
    expect(call?.path).toContain('/inbox/outbox/p-1')
  })
})

// The server would answer 409, so the control must not offer a click that
// cannot work.
test('a reply already being delivered offers no Undo', () => {
  renderWithProviders(<UndoSendPill pending={pending({ status: 'sending', cancellable: false })} />)

  expect(screen.queryByRole('button', { name: /undo/i })).not.toBeInTheDocument()
  expect(screen.getByText(/on its way/i)).toBeInTheDocument()
})

// The race this feature is defined by: the worker claimed the reply between the
// page rendering Undo and the click landing. Nothing is broken — the mail went.
test('a 409 explains that the reply already left', async () => {
  cancelStatus = 409
  renderWithProviders(<UndoSendPill pending={pending()} />)

  fireEvent.click(screen.getByRole('button', { name: /undo/i }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/too late/i)
  expect(alert).not.toHaveTextContent('409')
})

test('any other failure is surfaced rather than swallowed', async () => {
  cancelStatus = 500
  renderWithProviders(<UndoSendPill pending={pending()} />)

  fireEvent.click(screen.getByRole('button', { name: /undo/i }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/couldn't undo this reply/i)
})
