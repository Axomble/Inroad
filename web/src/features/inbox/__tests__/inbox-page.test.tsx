import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { InboxPage } from '../inbox-page'

// This screen is the whole point of the unified inbox — a subject that never
// renders, a scope click that never re-fetches, or an empty inbox that looks
// like a broken one are all invisible in a screenshot. The router mock below
// is a working store (not a static stub) so a real navigate → re-render →
// re-fetch round trip, matching contacts-page.test.tsx's pattern, is what's
// under test.

const router = vi.hoisted(() => {
  const listeners = new Set<() => void>()
  const state = {
    search: {} as Record<string, unknown>,
    listeners,
    subscribe: (cb: () => void) => {
      listeners.add(cb)
      return () => listeners.delete(cb)
    },
    navigate: (options: { search: (prev: Record<string, unknown>) => Record<string, unknown> }) => {
      state.search = options.search(state.search)
      for (const cb of listeners) cb()
      return Promise.resolve()
    },
  }
  return state
})

vi.mock('@tanstack/react-router', async () => {
  const { useSyncExternalStore } = await import('react')
  return {
    useSearch: () => useSyncExternalStore(router.subscribe, () => router.search),
    // Stable across renders, as TanStack's own `useNavigate` is.
    useNavigate: () => router.navigate,
  }
})

// SortMenu (the reply-class filter) is a Radix DropdownMenu, which drives
// open/close through pointer events jsdom doesn't fully implement; polyfill
// what it touches (same shim campaigns-page.test.tsx uses).
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: jsonHeaders })
}

type Mailbox = { id: string; email: string }
type Thread = {
  id: string
  mailbox_id: string
  campaign_id: string | null
  contact_id: string | null
  contact_email: string
  contact_first_name: string
  contact_last_name: string
  subject: string
  last_reply_class: string
  unread: boolean
  last_message_at: string
}

const PAGE_SIZE = 25

let mailboxes: Mailbox[]
let threads: Thread[]
let threadRequests: URL[]
let readRequests: { id: string; body: unknown }[]

function lastThreadRequest(): URL {
  const last = threadRequests[threadRequests.length - 1]
  if (!last) throw new Error('no /inbox/threads request was made')
  return last
}

/** `threads`, in the server's own newest-first order — index 0 is newest. */
function makeThread(i: number, overrides: Partial<Thread> = {}): Thread {
  return {
    id: `t-${i}`,
    mailbox_id: 'mb-1',
    campaign_id: null,
    contact_id: `ct-${i}`,
    contact_email: `contact${i}@acme.test`,
    contact_first_name: 'Contact',
    contact_last_name: `${i}`,
    subject: `Subject ${i}`,
    last_reply_class: 'neutral',
    unread: false,
    // Strictly decreasing, one minute apart, so a keyset cursor built from
    // any item unambiguously identifies "everything older".
    last_message_at: new Date(Date.UTC(2026, 7, 6, 12, 0, 0) - i * 60_000).toISOString(),
    ...overrides,
  }
}

/** Mirrors the real endpoint's keyset semantics against the in-memory fixture:
 * `threads` is already newest-first, so "before" a cursor's item is simply
 * everything after its index. */
function applyKeyset(items: Thread[], beforeLastMessageAt: string | null, beforeId: string | null): Thread[] {
  if (!beforeLastMessageAt || !beforeId) return items
  const index = items.findIndex((t) => t.last_message_at === beforeLastMessageAt && t.id === beforeId)
  return index === -1 ? items : items.slice(index + 1)
}

beforeEach(() => {
  router.search = {}
  threadRequests = []
  readRequests = []
  mailboxes = [
    { id: 'mb-1', email: 'sales@acme.test' },
    { id: 'mb-2', email: 'support@acme.test' },
  ]
  threads = [
    makeThread(0, {
      id: 't-1',
      contact_id: 'ct-1',
      contact_first_name: 'Jamie',
      contact_last_name: 'Lin',
      contact_email: 'jamie@prospect.test',
      subject: 'Re: intro',
      last_reply_class: 'positive',
      unread: true,
      mailbox_id: 'mb-1',
    }),
    makeThread(1, {
      id: 't-2',
      // A legacy direct-send match: no linked contact at all.
      contact_id: null,
      contact_first_name: '',
      contact_last_name: '',
      contact_email: '',
      subject: 'Re: follow up',
      last_reply_class: 'neutral',
      unread: false,
      mailbox_id: 'mb-2',
    }),
  ]

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const url = new URL(href, 'http://localhost')
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()

      if (url.pathname.endsWith('/mailboxes')) return json(mailboxes)

      const readMatch = /\/inbox\/threads\/([^/]+)\/read$/.exec(url.pathname)
      if (readMatch?.[1] && method === 'PUT') {
        const body = isRequest
          ? await input.clone().json()
          : typeof init?.body === 'string'
            ? JSON.parse(init.body)
            : {}
        readRequests.push({ id: readMatch[1], body })
        return json({})
      }

      if (url.pathname.endsWith('/inbox/threads') && method === 'GET') {
        threadRequests.push(url)
        const mailboxId = url.searchParams.get('mailbox_id')
        const replyClass = url.searchParams.get('reply_class')
        const q = url.searchParams.get('q')
        const limit = Number(url.searchParams.get('limit') ?? PAGE_SIZE)
        let items = threads
        if (mailboxId) items = items.filter((t) => t.mailbox_id === mailboxId)
        if (replyClass) items = items.filter((t) => t.last_reply_class === replyClass)
        if (q) {
          const needle = q.toLowerCase()
          items = items.filter(
            (t) => t.subject.toLowerCase().includes(needle) || t.contact_email.toLowerCase().includes(needle),
          )
        }
        items = applyKeyset(items, url.searchParams.get('before_last_message_at'), url.searchParams.get('before_id'))
        return json({ items: items.slice(0, limit) })
      }

      return json({ error: 'unhandled' }, 404)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('renders thread rows with the contact as the primary label, subject, reply class, and unread indicator', async () => {
  renderWithProviders(<InboxPage />)

  // Contact-linked row: name is the primary label, subject the secondary one.
  expect(await screen.findByText('Jamie Lin')).toBeInTheDocument()
  expect(screen.getByText(/Re: intro/)).toBeInTheDocument()
  expect(screen.getByText(/Positive/i)).toBeInTheDocument()
  // Legacy match (no linked contact): falls back to a neutral label instead
  // of rendering blank.
  expect(screen.getByText('Unknown sender')).toBeInTheDocument()
  expect(screen.getByText(/Re: follow up/)).toBeInTheDocument()
  expect(screen.getByText(/Neutral/i)).toBeInTheDocument()
  // The unread thread's contact label carries a screen-reader cue the read
  // one doesn't — color/weight alone never carries the state.
  expect(screen.getByText('Unread:', { exact: false })).toBeInTheDocument()
})

test('a contact-linked row shows the contact email when no name is set', async () => {
  threads = [makeThread(0, { contact_first_name: '', contact_last_name: '', contact_email: 'nameless@prospect.test' })]
  renderWithProviders(<InboxPage />)

  expect(await screen.findByText('nameless@prospect.test')).toBeInTheDocument()
})

test('filtering by mailbox re-fetches with the mailbox query param', async () => {
  renderWithProviders(<InboxPage />)
  await screen.findByText('Jamie Lin')

  fireEvent.click(screen.getByRole('button', { name: /support@acme\.test/i }))

  await waitFor(() => expect(router.search.mailbox).toBe('mb-2'))
  await waitFor(() => expect(lastThreadRequest().searchParams.get('mailbox_id')).toBe('mb-2'))
  // "Unknown sender" (mb-2's row) is already on screen from the initial
  // unfiltered load, so asserting its presence wouldn't prove the refetch
  // landed — wait for the OTHER thread (now out of scope) to actually leave.
  await waitFor(() => expect(screen.queryByText('Jamie Lin')).not.toBeInTheDocument())
  expect(screen.getByText('Unknown sender')).toBeInTheDocument()
})

test('the reply-class filter re-fetches with the reply_class query param', async () => {
  renderWithProviders(<InboxPage />)
  await screen.findByText('Jamie Lin')

  // Radix's DropdownMenu opens on keydown (Enter), not a bare click.
  fireEvent.keyDown(screen.getByRole('button', { name: /^sort by all replies$/i }), { key: 'Enter' })
  fireEvent.click(await screen.findByRole('menuitem', { name: /^positive$/i }))

  await waitFor(() => expect(router.search.class).toBe('positive'))
  await waitFor(() => expect(lastThreadRequest().searchParams.get('reply_class')).toBe('positive'))
  // Same reasoning as the mailbox filter test.
  await waitFor(() => expect(screen.queryByText('Unknown sender')).not.toBeInTheDocument())
  expect(screen.getByText('Jamie Lin')).toBeInTheDocument()
})

test('typing a search debounces into one real, server-side q request', async () => {
  renderWithProviders(<InboxPage />)
  await screen.findByText('Jamie Lin')
  const requestsAtStart = threadRequests.length

  fireEvent.change(screen.getByRole('searchbox', { name: /search subject or contact email/i }), {
    target: { value: 'jamie' },
  })

  // Echoed immediately — the field never waits on the request.
  expect(screen.getByRole('searchbox', { name: /search subject or contact email/i })).toHaveValue('jamie')

  await waitFor(() => expect(router.search.q).toBe('jamie'), { timeout: 5000 })
  await waitFor(() => expect(lastThreadRequest().searchParams.get('q')).toBe('jamie'), { timeout: 5000 })
  expect(threadRequests.length).toBeGreaterThan(requestsAtStart)
  await waitFor(() => expect(screen.queryByText('Unknown sender')).not.toBeInTheDocument())
  expect(screen.getByText('Jamie Lin')).toBeInTheDocument()
})

test('empty state when no threads exist', async () => {
  threads = []
  renderWithProviders(<InboxPage />)

  expect(await screen.findByText('No replies yet')).toBeInTheDocument()
})

test('opening an unread thread marks it read', async () => {
  renderWithProviders(<InboxPage />)
  const row = await screen.findByText('Jamie Lin')

  fireEvent.click(row)

  await waitFor(() => expect(readRequests).toEqual([{ id: 't-1', body: { unread: false } }]))
})

test('clicking an already-read thread does not fire a mark-read request', async () => {
  renderWithProviders(<InboxPage />)
  const row = await screen.findByText('Unknown sender')

  fireEvent.click(row)

  // Give the (absent) request a chance to prove itself wrong.
  await new Promise((resolve) => setTimeout(resolve, 50))
  expect(readRequests).toEqual([])
})

test('j/k moves the keyboard cursor and Enter opens (marks read) the row under it', async () => {
  renderWithProviders(<InboxPage />)
  await screen.findByText('Jamie Lin')

  fireEvent.keyDown(document, { key: 'j' })
  fireEvent.keyDown(document, { key: 'Enter' })

  await waitFor(() => expect(readRequests).toEqual([{ id: 't-1', body: { unread: false } }]))
})

test('Next is disabled on a partial page, enabled on a full one, and paging forward fetches with the last row\'s real cursor', async () => {
  // 26 threads: the first page (limit 25) comes back full (enables Next); the
  // second page comes back with exactly 1 (a partial page — definitively no
  // more), which must disable Next.
  threads = Array.from({ length: 26 }, (_, i) => makeThread(i))
  renderWithProviders(<InboxPage />)

  await screen.findByText('Contact 0')
  expect(screen.getByRole('button', { name: 'Next page' })).toBeEnabled()

  fireEvent.click(screen.getByRole('button', { name: 'Next page' }))

  await waitFor(() => expect(screen.getByText('Contact 25')).toBeInTheDocument())
  // The request that landed page 2 carried the LAST row of page 1's own
  // last_message_at/id as the keyset cursor — not a server-issued token.
  const page2 = lastThreadRequest()
  expect(page2.searchParams.get('before_id')).toBe('t-24')
  expect(page2.searchParams.get('before_last_message_at')).toBe(threads[24]?.last_message_at)
  // A single-item page is definitively the last one.
  expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
})

test('Previous pops back to the exact prior page', async () => {
  threads = Array.from({ length: 26 }, (_, i) => makeThread(i))
  renderWithProviders(<InboxPage />)
  await screen.findByText('Contact 0')

  fireEvent.click(screen.getByRole('button', { name: 'Next page' }))
  await screen.findByText('Contact 25')

  fireEvent.click(screen.getByRole('button', { name: 'Previous page' }))

  await waitFor(() => expect(screen.getByText('Contact 0')).toBeInTheDocument())
  expect(screen.queryByText('Contact 25')).not.toBeInTheDocument()
  // Back at the floor: no cursor left to walk back through.
  expect(router.search.cursor).toBeUndefined()
  expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled()
  // Page 1 was a full page, so Next is live again.
  expect(screen.getByRole('button', { name: 'Next page' })).toBeEnabled()
})
