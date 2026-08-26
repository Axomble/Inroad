import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { SnoozeMenu } from '../snooze-menu'
import type { InboxSnooze } from '../api'

// The menu is a Radix DropdownMenu, which drives open/close through pointer
// events jsdom doesn't fully implement; polyfill what it touches (the same shim
// inbox-page.test.tsx uses).
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }

interface SnoozeCall {
  method: string
  path: string
  body: string
}

let calls: SnoozeCall[]
let snoozeStatus: number

beforeEach(() => {
  calls = []
  snoozeStatus = 200
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const url = new URL(href, 'http://localhost')
      const method = (isRequest ? input.method : (init?.method ?? 'GET')).toUpperCase()
      // RTK Query's fetch base query passes a Request, so the payload is on the
      // Request rather than in `init` — read both, or the assertion on what was
      // actually sent has nothing to inspect.
      const body = isRequest ? await input.clone().text() : typeof init?.body === 'string' ? init.body : ''
      calls.push({ method, path: url.pathname, body })

      if (url.pathname.includes('/snooze')) {
        if (snoozeStatus !== 200 && snoozeStatus !== 204) {
          return new Response(JSON.stringify({ error: 'nope' }), { status: snoozeStatus, headers: jsonHeaders })
        }
        if (method === 'DELETE') return new Response(null, { status: 204 })
        return new Response(
          JSON.stringify({
            thread_id: 't-1',
            snooze_until: new Date(Date.now() + 86_400_000).toISOString(),
            snoozed_by: null,
            created_at: new Date().toISOString(),
          }),
          { status: 200, headers: jsonHeaders },
        )
      }
      return new Response(JSON.stringify({ error: 'unhandled' }), { status: 404, headers: jsonHeaders })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function snoozeCalls(): SnoozeCall[] {
  return calls.filter((c) => c.path.includes('/snooze'))
}

/**
 * Opens the menu the way these tests must: Radix drives open/close from a
 * pointer sequence jsdom does not fully implement, but it also opens on
 * Enter/Space from the trigger — the same route a keyboard user takes. Matches
 * how inbox-page.test.tsx opens its SortMenu.
 */
function openMenu(name: RegExp) {
  fireEvent.keyDown(screen.getByRole('button', { name }), { key: 'Enter' })
}

test('an unsnoozed thread offers a Snooze button whose menu lists presets', async () => {
  renderWithProviders(<SnoozeMenu threadId="t-1" snooze={null} />)

  openMenu(/snooze this thread/i)

  expect(await screen.findByText('Tomorrow')).toBeInTheDocument()
  expect(screen.getByText('Next week')).toBeInTheDocument()
})

test('choosing a preset PUTs an absolute RFC3339 instant', async () => {
  renderWithProviders(<SnoozeMenu threadId="t-1" snooze={null} />)
  openMenu(/snooze this thread/i)
  fireEvent.click(await screen.findByText('Tomorrow'))

  await waitFor(() => expect(snoozeCalls()).toHaveLength(1))
  const call = snoozeCalls()[0]
  if (!call) throw new Error('no snooze call')
  expect(call.method).toBe('PUT')
  expect(call.path).toContain('/inbox/threads/t-1/snooze')

  // A UTC instant, not a local wall-clock string: the moment is absolute and
  // only its display is local.
  const sent = JSON.parse(call.body) as { snooze_until: string }
  expect(sent.snooze_until).toMatch(/Z$/)
  expect(new Date(sent.snooze_until).getTime()).toBeGreaterThan(Date.now())
})

test('an already-snoozed thread shows its return time and offers Unsnooze', async () => {
  const snooze: InboxSnooze = {
    thread_id: 't-1',
    snooze_until: new Date(Date.now() + 86_400_000).toISOString(),
    snoozed_by: null,
    created_at: new Date().toISOString(),
  }
  renderWithProviders(<SnoozeMenu threadId="t-1" snooze={snooze} />)

  openMenu(/snoozed until/i)

  fireEvent.click(await screen.findByText('Unsnooze now'))
  await waitFor(() => expect(snoozeCalls()).toHaveLength(1))
  expect(snoozeCalls()[0]?.method).toBe('DELETE')
})

test('a custom moment in the past is refused inline, without a request', async () => {
  renderWithProviders(<SnoozeMenu threadId="t-1" snooze={null} />)
  openMenu(/snooze this thread/i)

  const field = await screen.findByLabelText(/specific date and time/i)
  fireEvent.change(field, { target: { value: '2020-01-01T09:00' } })
  fireEvent.click(screen.getByRole('button', { name: 'Snooze' }))

  expect(await screen.findByRole('alert')).toHaveTextContent(/future/i)
  // Client validation is a courtesy, but it must also avoid a pointless 422.
  expect(snoozeCalls()).toHaveLength(0)
})

test('a custom moment beyond 90 days is refused inline, naming the bound', async () => {
  renderWithProviders(<SnoozeMenu threadId="t-1" snooze={null} />)
  openMenu(/snooze this thread/i)

  const far = new Date(Date.now() + 200 * 86_400_000)
  const pad = (n: number) => String(n).padStart(2, '0')
  const value = `${far.getFullYear()}-${pad(far.getMonth() + 1)}-${pad(far.getDate())}T09:00`

  fireEvent.change(await screen.findByLabelText(/specific date and time/i), { target: { value } })
  fireEvent.click(screen.getByRole('button', { name: 'Snooze' }))

  expect(await screen.findByRole('alert')).toHaveTextContent('90')
  expect(snoozeCalls()).toHaveLength(0)
})

// A failed snooze must be visible: the operator believes the thread is parked,
// and silence here would lose it.
test('a server failure is surfaced, not swallowed', async () => {
  snoozeStatus = 500
  renderWithProviders(<SnoozeMenu threadId="t-1" snooze={null} />)
  openMenu(/snooze this thread/i)
  fireEvent.click(await screen.findByText('Tomorrow'))

  expect(await screen.findByRole('alert')).toHaveTextContent(/couldn't update the snooze/i)
})

test('a 422 explains the bound rather than showing a bare status', async () => {
  snoozeStatus = 422
  renderWithProviders(<SnoozeMenu threadId="t-1" snooze={null} />)
  openMenu(/snooze this thread/i)
  fireEvent.click(await screen.findByText('Tomorrow'))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/within 90 days/i)
  expect(alert).not.toHaveTextContent('422')
})
