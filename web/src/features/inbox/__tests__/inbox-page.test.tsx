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
    // The one `navigate({ to, params })` call a row click or Enter makes —
    // distinct from `search`, which is this screen's OWN url-patch style
    // navigation (filters, paging). A real `useNavigate()` handles both
    // shapes; this stub mirrors that instead of assuming only one is ever
    // used, which is what let the list's dead row-click go unnoticed.
    lastNavigation: null as { to: string; params?: Record<string, unknown> } | null,
    listeners,
    subscribe: (cb: () => void) => {
      listeners.add(cb)
      return () => listeners.delete(cb)
    },
    navigate: (
      options:
        | { search: (prev: Record<string, unknown>) => Record<string, unknown> }
        | { to: string; params?: Record<string, unknown> },
    ) => {
      if ('search' in options) {
        state.search = options.search(state.search)
        for (const cb of listeners) cb()
      } else {
        state.lastNavigation = { to: options.to, params: options.params }
      }
      return Promise.resolve()
    },
  }
  return state
})

vi.mock('@tanstack/react-router', async () => {
  const { useSyncExternalStore, createElement } = await import('react')
  return {
    useSearch: () => useSyncExternalStore(router.subscribe, () => router.search),
    // Stable across renders, as TanStack's own `useNavigate` is.
    useNavigate: () => router.navigate,
    // The reader's ReplyComposer links to Settings → AI when no model is
    // configured. A plain anchor is enough: no test here asserts on it, but
    // omitting the export makes the whole reader throw on render.
    Link: ({ to, children, ...rest }: { to: string; children?: unknown }) =>
      createElement('a', { href: to, ...rest }, children as never),
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
let overviewRequests: URL[]
/** Lets a test force the overview to fail, to assert the rail degrades. */
let overviewStatus: number

/** The reply-class filter's options now come from GET /reply-labels, not a
 * hardcoded list — the fixture mirrors the built-in taxonomy so filter tests
 * written against those keys/labels keep working. */
function replyLabel(key: string, label: string, position: number) {
  return {
    id: `label-${key}`,
    key,
    label,
    color: '#888888',
    position,
    is_builtin: true,
    stops_enrollment: false,
    is_automated: false,
    suppresses_contact: false,
    captures_deal: false,
    created_at: '',
    updated_at: '',
  }
}
const REPLY_LABELS = {
  labels: [
    replyLabel('positive', 'Positive', 0),
    replyLabel('negative', 'Negative', 1),
    replyLabel('neutral', 'Neutral', 2),
  ],
}

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
  router.lastNavigation = null
  threadRequests = []
  overviewRequests = []
  overviewStatus = 200
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
      if (url.pathname.endsWith('/reply-labels')) return json(REPLY_LABELS)

      if (url.pathname.endsWith('/inbox/overview')) {
        overviewRequests.push(url)
        if (overviewStatus !== 200) return json({ error: 'nope' }, overviewStatus)
        // Derived from the same `threads` fixture the list serves, so a count
        // asserted in a test can never contradict the rows beside it.
        return json({
          total: threads.length,
          unread: threads.filter((t) => t.unread).length,
          today: threads.length,
          this_week: threads.length,
          awaiting_reply: threads.filter((t) => t.unread).length,
          by_mailbox: mailboxes.map((m) => ({
            mailbox_id: m.id,
            total: threads.filter((t) => t.mailbox_id === m.id).length,
            unread: threads.filter((t) => t.mailbox_id === m.id && t.unread).length,
          })),
          by_reply_class: [],
        })
      }

      // One thread's detail, for the three-pane reader. Its message history is
      // synthesized from the summary so the reader has something to render.
      const detailMatch = /\/inbox\/threads\/([^/]+)$/.exec(url.pathname)
      if (detailMatch && method === 'GET') {
        const thread = threads.find((t) => t.id === detailMatch[1])
        if (!thread) return json({ error: 'not found' }, 404)
        return json({
          ...thread,
          messages: [
            {
              direction: 'inbound',
              message_id: `m-${thread.id}`,
              from_email: thread.contact_email || 'someone@prospect.test',
              from_name: thread.contact_first_name,
              to_email: 'sales@acme.test',
              subject: thread.subject,
              body_text: `Body of ${thread.subject}`,
              body_html: '',
              reply_class: thread.last_reply_class,
              occurred_at: thread.last_message_at,
            },
          ],
        })
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

test('clicking a thread row navigates to its own thread route, not a mark-read call', async () => {
  renderWithProviders(<InboxPage />)
  const row = await screen.findByText('Jamie Lin')

  fireEvent.click(row)

  // The reader route (`/app/inbox/$threadId`) is the single source of truth
  // for marking a thread read on open — the list must only navigate.
  await waitFor(() => expect(router.lastNavigation).toEqual({ to: '/app/inbox/$threadId', params: { threadId: 't-1' } }))
})

test('clicking an already-read thread still navigates to it', async () => {
  renderWithProviders(<InboxPage />)
  const row = await screen.findByText('Unknown sender')

  fireEvent.click(row)

  await waitFor(() => expect(router.lastNavigation).toEqual({ to: '/app/inbox/$threadId', params: { threadId: 't-2' } }))
})

test('j/k moves the keyboard cursor and Enter navigates to the row under it', async () => {
  renderWithProviders(<InboxPage />)
  await screen.findByText('Jamie Lin')

  fireEvent.keyDown(document, { key: 'j' })
  fireEvent.keyDown(document, { key: 'Enter' })

  await waitFor(() => expect(router.lastNavigation).toEqual({ to: '/app/inbox/$threadId', params: { threadId: 't-1' } }))
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

test('the reply-class filter options are the workspace reply labels, not a hardcoded legacy list', async () => {
  renderWithProviders(<InboxPage />)
  await screen.findByText('Jamie Lin')

  fireEvent.keyDown(screen.getByRole('button', { name: /^sort by all replies$/i }), { key: 'Enter' })

  expect(await screen.findByRole('menuitem', { name: /^positive$/i })).toBeInTheDocument()
  expect(screen.getByRole('menuitem', { name: /^negative$/i })).toBeInTheDocument()
  expect(screen.getByRole('menuitem', { name: /^neutral$/i })).toBeInTheDocument()
  // "Out of office" is one of the old hardcoded built-ins but isn't in this
  // workspace's own /reply-labels fixture, so it must not appear.
  expect(screen.queryByRole('menuitem', { name: /out of office/i })).not.toBeInTheDocument()
})

test('while reply labels are still loading, the filter degrades to just "All replies" rather than blocking the page', async () => {
  const pendingReplyLabels = new Promise<Response>(() => {})
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const url = new URL(href, 'http://localhost')
      if (url.pathname.endsWith('/mailboxes')) return json(mailboxes)
      if (url.pathname.endsWith('/reply-labels')) return pendingReplyLabels
      if (url.pathname.endsWith('/inbox/threads')) return json({ items: threads })
      return json({ error: 'unhandled' }, 404)
    }),
  )

  renderWithProviders(<InboxPage />)
  await screen.findByText('Jamie Lin')

  fireEvent.keyDown(screen.getByRole('button', { name: /^sort by all replies$/i }), { key: 'Enter' })
  expect(await screen.findByRole('menuitem', { name: 'All replies' })).toBeInTheDocument()
  expect(screen.queryByRole('menuitem', { name: /^positive$/i })).not.toBeInTheDocument()
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

// ---------------------------------------------------------------------------
// Scope rail + real counts (the overview endpoint)
// ---------------------------------------------------------------------------

test('the rail renders real per-mailbox counts from the overview, not a client-side sample', async () => {
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')

  await waitFor(() => expect(overviewRequests.length).toBeGreaterThan(0))
  // One thread per mailbox in the fixture, and mb-1's is the unread one.
  const salesRow = screen.getByRole('button', { name: /sales@acme\.test/ })
  expect(salesRow).toHaveTextContent('1')
  expect(salesRow).toHaveTextContent('unread')
})

test('the overview is asked for the viewer own timezone offset, so "today" is their day', async () => {
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')

  await waitFor(() => expect(overviewRequests.length).toBeGreaterThan(0))
  const request = overviewRequests[overviewRequests.length - 1]
  if (!request) throw new Error('no overview request')
  expect(request.searchParams.get('tz_offset')).toBe(String(-new Date().getTimezoneOffset()))
})

test('a failed overview degrades to a rail without counts rather than breaking the page', async () => {
  overviewStatus = 500
  renderWithProviders(<InboxPage />)

  // The list still renders — the counts are supplementary, not load-bearing.
  await screen.findByText('Re: intro')
  expect(await screen.findByText(/Counts unavailable/)).toBeInTheDocument()
})

test('clicking a virtual scope re-fetches with that scope and drops any mailbox filter', async () => {
  router.search = { mailbox: 'mb-1' }
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')

  fireEvent.click(screen.getByRole('button', { name: /Awaiting reply/ }))

  await waitFor(() => {
    const url = lastThreadRequest()
    expect(url.searchParams.get('scope')).toBe('awaiting_reply')
    // The rail shows one selection: picking a folder must clear the mailbox,
    // not silently intersect the two.
    expect(url.searchParams.get('mailbox_id')).toBeNull()
  })
})

test('the All mail scope sends no scope param, since it is the API default', async () => {
  router.search = { scope: 'unread' }
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')

  fireEvent.click(screen.getByRole('button', { name: /All mail/ }))

  await waitFor(() => expect(lastThreadRequest().searchParams.get('scope')).toBeNull())
})

test('picking a mailbox clears the virtual scope', async () => {
  router.search = { scope: 'unread' }
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')

  fireEvent.click(screen.getByRole('button', { name: /support@acme\.test/ }))

  await waitFor(() => {
    const url = lastThreadRequest()
    expect(url.searchParams.get('mailbox_id')).toBe('mb-2')
    expect(url.searchParams.get('scope')).toBeNull()
  })
})

test('an unrecognised scope in the URL degrades to the whole inbox instead of a 400', async () => {
  router.search = { scope: 'not-a-scope' }
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')

  expect(lastThreadRequest().searchParams.get('scope')).toBeNull()
})

test('threads are grouped under time-bucket headings', async () => {
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')

  // The fixture's timestamps are a fixed 2026-08-06, which is in the past
  // relative to any real run, so they land in the oldest bucket. The assertion
  // is that a heading exists at all — which bucket is bucketFor's own test.
  const headings = await screen.findAllByRole('heading', { level: 3 })
  expect(headings.length).toBeGreaterThan(0)
})

test('a scope change resets paging, so page 2 of one folder is not carried into another', async () => {
  threads = Array.from({ length: PAGE_SIZE }, (_, i) => makeThread(i))
  renderWithProviders(<InboxPage />)
  await screen.findByText('Subject 0')

  fireEvent.click(screen.getByRole('button', { name: 'Next page' }))
  await waitFor(() => expect(lastThreadRequest().searchParams.get('before_id')).not.toBeNull())

  fireEvent.click(screen.getByRole('button', { name: /Unread/ }))
  await waitFor(() => {
    const url = lastThreadRequest()
    expect(url.searchParams.get('scope')).toBe('unread')
    expect(url.searchParams.get('before_id')).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// Three-pane layout. jsdom has no matchMedia, so the tests above all exercise
// the NARROW path (a row click navigates to the thread's own route). These
// stub it to assert the wide path selects in place instead.
// ---------------------------------------------------------------------------

/** Stubs matchMedia so every query reports `matches`, putting the page in its
 * three-pane layout. Returns a working (if inert) MediaQueryList. */
function stubWideViewport() {
  vi.stubGlobal(
    'matchMedia',
    vi.fn((query: string) => ({
      matches: true,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    })),
  )
}

test('on a wide viewport a row click opens the thread in the reader pane instead of navigating away', async () => {
  stubWideViewport()
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')

  // Before any selection the pane prompts rather than showing a blank column.
  expect(screen.getByText('Select a thread to read it.')).toBeInTheDocument()

  fireEvent.click(screen.getByText('Re: intro'))

  // The reader fetches the thread; navigation must NOT have happened.
  await waitFor(() => expect(screen.queryByText('Select a thread to read it.')).not.toBeInTheDocument())
  expect(router.lastNavigation).toBeNull()
})

test('the row open in the reader is marked as current, so it is distinguishable from the hover cursor', async () => {
  stubWideViewport()
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')

  fireEvent.click(screen.getByText('Re: intro'))

  await waitFor(() => {
    const current = document.querySelectorAll('li[aria-current="true"]')
    expect(current).toHaveLength(1)
    expect(current[0]).toHaveTextContent('Re: intro')
  })
})

test('a filter change that drops the selected thread clears the reader rather than showing a stale one', async () => {
  stubWideViewport()
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')

  // t-1 lives in mb-1.
  fireEvent.click(screen.getByText('Re: intro'))
  await waitFor(() => expect(screen.queryByText('Select a thread to read it.')).not.toBeInTheDocument())

  // Switching to mb-2 removes it from the list entirely.
  fireEvent.click(screen.getByRole('button', { name: /support@acme\.test/ }))

  await waitFor(() => expect(screen.getByText('Select a thread to read it.')).toBeInTheDocument())
})

test('switching threads in the reader pane discards the previous thread typed reply', async () => {
  stubWideViewport()
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')

  // Draft a reply to thread A but never send it.
  fireEvent.click(screen.getByText('Re: intro'))
  const composer = await screen.findByPlaceholderText('Write a reply…')
  fireEvent.change(composer, { target: { value: 'Private note meant only for Jamie' } })
  expect(screen.getByPlaceholderText('Write a reply…')).toHaveValue('Private note meant only for Jamie')

  // Move to thread B. The composer must come up empty: carrying the text over
  // would let Send deliver Jamie's reply to a different contact.
  fireEvent.click(screen.getByText('Re: follow up'))

  await waitFor(() => expect(screen.getByPlaceholderText('Write a reply…')).toHaveValue(''))
})

test('tz_offset is sent only for the calendar-dependent scopes', async () => {
  router.search = { scope: 'today' }
  renderWithProviders(<InboxPage />)
  await screen.findByText('Re: intro')
  await waitFor(() =>
    expect(lastThreadRequest().searchParams.get('tz_offset')).toBe(String(-new Date().getTimezoneOffset())),
  )

  // awaiting_reply has no calendar boundary, so the offset would only be
  // cache-key noise.
  fireEvent.click(screen.getByRole('button', { name: /Awaiting reply/ }))
  await waitFor(() => {
    const url = lastThreadRequest()
    expect(url.searchParams.get('scope')).toBe('awaiting_reply')
    expect(url.searchParams.get('tz_offset')).toBeNull()
  })
})
