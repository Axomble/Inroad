import { screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { ThreadDetailPage } from '../thread-detail-page'

// The reader's identity is a URL param (`/app/inbox/$threadId`), read via
// `getRouteApi(...).useParams()` — no real router is under test here (that's
// the route file's job), so both it and the back `Link` are stubbed the same
// way `app-sidebar.test.tsx` stubs `Link`.
let currentThreadId = 't-1'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, ...props }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
  getRouteApi: () => ({ useParams: () => ({ threadId: currentThreadId }) }),
}))

const jsonHeaders = { 'content-type': 'application/json' }

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: jsonHeaders })
}

type Message = {
  direction: 'inbound' | 'outbound'
  message_id: string
  from_email: string
  from_name: string
  to_email: string
  subject: string
  body_text: string
  body_html: string
  reply_class: string
  occurred_at: string
}

type ThreadDetail = {
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
  messages: Message[]
}

function makeMessage(overrides: Partial<Message> = {}): Message {
  return {
    direction: 'inbound',
    message_id: 'm-1',
    from_email: 'jamie@prospect.test',
    from_name: 'Jamie Lin',
    to_email: 'sales@acme.test',
    subject: 'Re: intro',
    body_text: 'A message body',
    body_html: '',
    reply_class: 'positive',
    occurred_at: '2026-08-06T12:00:00.000Z',
    ...overrides,
  }
}

let thread: ThreadDetail | null
let readRequests: { id: string; body: unknown }[]
let threadFetchCount: number

beforeEach(() => {
  currentThreadId = 't-1'
  readRequests = []
  threadFetchCount = 0
  thread = {
    id: 't-1',
    mailbox_id: 'mb-1',
    campaign_id: null,
    contact_id: 'ct-1',
    contact_email: 'jamie@prospect.test',
    contact_first_name: 'Jamie',
    contact_last_name: 'Lin',
    subject: 'Re: intro',
    last_reply_class: 'positive',
    // The fixture never flips this on the mutation's own success (mirroring a
    // real server whose read-state hasn't reflected the write yet by the time
    // the invalidation-driven refetch lands) — the one scenario the mark-read
    // guard actually has to survive.
    unread: true,
    last_message_at: '2026-08-06T12:00:00.000Z',
    messages: [
      makeMessage({
        message_id: 'm-1',
        direction: 'outbound',
        from_name: '',
        from_email: 'sales@acme.test',
        body_text: 'Hi Jamie, following up on my last note',
        occurred_at: '2026-08-06T11:00:00.000Z',
      }),
      makeMessage({
        message_id: 'm-2',
        direction: 'inbound',
        body_text: 'Sounds good, let’s talk Thursday',
        occurred_at: '2026-08-06T12:00:00.000Z',
      }),
    ],
  }

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const url = new URL(href, 'http://localhost')
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()

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

      const threadMatch = /\/inbox\/threads\/([^/]+)$/.exec(url.pathname)
      if (threadMatch?.[1] && method === 'GET') {
        threadFetchCount += 1
        if (!thread || threadMatch[1] !== thread.id) return json({ error: 'thread not found' }, 404)
        return json(thread)
      }

      return json({ error: 'unhandled' }, 404)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('renders inbound and outbound messages in occurred_at order', async () => {
  renderWithProviders(<ThreadDetailPage />)

  expect(await screen.findByText('Hi Jamie, following up on my last note')).toBeInTheDocument()
  expect(screen.getByText('Sounds good, let’s talk Thursday')).toBeInTheDocument()

  const bodies = screen.getAllByText(/Hi Jamie, following up on my last note|Sounds good, let’s talk Thursday/)
  // The outbound send occurred first (11:00) and the inbound reply after
  // (12:00) — the API hands both back already interleaved oldest-first, and
  // nothing here re-sorts them, so the DOM order must match.
  expect(bodies[0]).toHaveTextContent('Hi Jamie, following up on my last note')
  expect(bodies[1]).toHaveTextContent('Sounds good, let’s talk Thursday')

  // The header identity reuses the same contact-display rule as the thread
  // list (a second "Jamie Lin" is the inbound message's own from_name).
  expect(screen.getAllByText('Jamie Lin').length).toBeGreaterThan(0)
})

test('renders multiple outbound messages with a blank message_id and reply_class without collision or an "unknown" state', async () => {
  // The backend only records a provider Message-ID once a send is actually
  // delivered, and never classifies a reply_class on an outbound leg at
  // all — a real thread with two sequence steps sent before either got a
  // reply legitimately has two outbound messages that are both `""` on
  // both fields. `message_id` was the React key here; two blanks used to
  // collide and only one bubble would keep rendering.
  if (!thread) throw new Error('fixture not set')
  thread.messages = [
    makeMessage({
      message_id: '',
      direction: 'outbound',
      reply_class: '',
      from_name: '',
      from_email: 'sales@acme.test',
      body_text: 'Step 1: quick intro',
      occurred_at: '2026-08-06T10:00:00.000Z',
    }),
    makeMessage({
      message_id: '',
      direction: 'outbound',
      reply_class: '',
      from_name: '',
      from_email: 'sales@acme.test',
      body_text: 'Step 2: following up',
      occurred_at: '2026-08-06T10:05:00.000Z',
    }),
  ]

  renderWithProviders(<ThreadDetailPage />)

  expect(await screen.findByText('Step 1: quick intro')).toBeInTheDocument()
  // The real bug: a `key` collision on two blank `message_id`s means React
  // reconciles the second bubble into the first's slot instead of mounting
  // a sibling — this would silently disappear rather than throw.
  expect(screen.getByText('Step 2: following up')).toBeInTheDocument()
  // A blank `reply_class` on an outbound bubble is not "missing data" — an
  // outbound send is never classified — so it must render as a plain "You"
  // bubble, never an "Unknown" badge.
  expect(screen.getAllByText('You')).toHaveLength(2)
  expect(screen.queryByText(/unknown/i)).not.toBeInTheDocument()
})

test('mark-read fires exactly once per thread open, even across the background refetch its own mutation triggers', async () => {
  renderWithProviders(<ThreadDetailPage />)
  await screen.findByText('Sounds good, let’s talk Thursday')

  await waitFor(() => expect(readRequests).toEqual([{ id: 't-1', body: { unread: false } }]))

  // `setInboxThreadRead` invalidates the thread's own cache tag, so RTK Query
  // answers with a real background refetch — not a stub — of `getInboxThread`
  // for the same id. Wait for that second GET to actually land...
  await waitFor(() => expect(threadFetchCount).toBeGreaterThanOrEqual(2))
  // ...then confirm it did NOT re-trigger the mark-read mutation, which a
  // guard keyed on the query result's identity (rather than the thread id)
  // would have let slip through into a refetch → mark-read → refetch loop.
  expect(readRequests).toHaveLength(1)
})

test('an already-read thread never fires the mark-read mutation', async () => {
  if (thread) thread.unread = false
  renderWithProviders(<ThreadDetailPage />)
  await screen.findByText('Sounds good, let’s talk Thursday')

  await new Promise((resolve) => setTimeout(resolve, 50))
  expect(readRequests).toEqual([])
})

test('a 404 thread (deleted or cross-tenant) shows the standard not-found screen, not a blank page', async () => {
  currentThreadId = 'missing-thread'
  renderWithProviders(<ThreadDetailPage />)

  expect(await screen.findByText(/page not found/i)).toBeInTheDocument()
})
