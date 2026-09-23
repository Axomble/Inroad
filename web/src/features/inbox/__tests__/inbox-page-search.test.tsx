import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { InboxPage } from '../inbox-page'

// The inbox's search box, end to end through the real store: the debounce,
// the URL as the source of truth, the facets riding along, "Load more" over
// next_cursor, and every non-happy state. The router is a working store (not a
// static stub), mirroring inbox-page.test.tsx, so a navigate → re-render →
// re-fetch round trip is what's under test.

const router = vi.hoisted(() => {
  const listeners = new Set<() => void>()
  const state = {
    search: {} as Record<string, unknown>,
    lastNavigation: null as { to: string; params?: Record<string, unknown> } | null,
    /** Whether each search-param write pushed a history entry or replaced one. */
    writes: [] as { replace: boolean | undefined; search: Record<string, unknown> }[],
    subscribe: (cb: () => void) => {
      listeners.add(cb)
      return () => listeners.delete(cb)
    },
    navigate: (
      options:
        | { search: (prev: Record<string, unknown>) => Record<string, unknown>; replace?: boolean }
        | { to: string; params?: Record<string, unknown> },
    ) => {
      if ('search' in options) {
        state.search = options.search(state.search)
        state.writes.push({ replace: options.replace, search: state.search })
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
    useNavigate: () => router.navigate,
    Link: ({ to, children, ...rest }: { to: string; children?: unknown }) =>
      createElement('a', { href: to, ...rest }, children as never),
  }
})

beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

type Segment = { text: string; match: boolean }

function thread(i: number, overrides: Record<string, unknown> = {}) {
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
    reply_label: null,
    unread: false,
    last_message_at: new Date(Date.UTC(2026, 8, 20, 12, 0, 0) - i * 60_000).toISOString(),
    ...overrides,
  }
}

function hit(
  i: number,
  {
    legs = ['inbound'],
    direction = 'inbound',
    subject = [{ text: `Re: Subject ${i}`, match: false }],
    body = [
      { text: 'Happy to set up a ', match: false },
      { text: 'meeting', match: true },
      { text: ' next week.', match: false },
    ],
    threadOverrides = {},
  }: {
    legs?: ('inbound' | 'outbound' | 'contact')[]
    direction?: 'inbound' | 'outbound'
    subject?: Segment[]
    body?: Segment[]
    threadOverrides?: Record<string, unknown>
  } = {},
) {
  const t = thread(i, threadOverrides)
  return {
    thread: t,
    matched_legs: legs,
    snippet: { direction, occurred_at: t.last_message_at, subject, body },
  }
}

type SearchResponse = { status: number; body: unknown; headers?: Record<string, string> }

let searchRequests: URL[]
let threadRequests: URL[]
/** Answers GET /inbox/search. Defaults to one page of two hits. */
let respondToSearch: (url: URL) => SearchResponse

function lastSearch(): URL {
  const last = searchRequests[searchRequests.length - 1]
  if (!last) throw new Error('no /inbox/search request was made')
  return last
}

beforeEach(() => {
  router.search = {}
  router.lastNavigation = null
  router.writes = []
  searchRequests = []
  threadRequests = []
  respondToSearch = () => ({
    status: 200,
    body: {
      items: [
        hit(0, { legs: ['inbound', 'outbound'], threadOverrides: { contact_first_name: 'Jamie', contact_last_name: 'Lin' } }),
        hit(1, {
          legs: ['outbound'],
          direction: 'outbound',
          subject: [
            { text: 'Our ', match: false },
            { text: 'meeting', match: true },
            { text: ' notes', match: false },
          ],
          body: [{ text: 'Notes from our call.', match: false }],
          threadOverrides: { subject: 'Call notes', contact_first_name: 'Riley', contact_last_name: 'Park' },
        }),
      ],
      next_cursor: null,
    },
  })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const url = new URL(href, 'http://localhost')
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()

      if (url.pathname.endsWith('/mailboxes')) {
        return json([
          { id: 'mb-1', email: 'sales@acme.test' },
          { id: 'mb-2', email: 'support@acme.test' },
        ])
      }
      if (url.pathname.endsWith('/reply-labels')) return json({ labels: [] })
      if (url.pathname.endsWith('/inbox/labels')) return json({ labels: [] })
      if (url.pathname.endsWith('/inbox/overview')) {
        return json({ total: 2, unread: 0, today: 2, this_week: 2, awaiting_reply: 0, by_mailbox: [], by_reply_class: [] })
      }
      if (url.pathname.endsWith('/inbox/search') && method === 'GET') {
        searchRequests.push(url)
        const { status, body, headers } = respondToSearch(url)
        return new Response(JSON.stringify(body), {
          status,
          headers: { 'content-type': 'application/json', ...headers },
        })
      }
      if (url.pathname.endsWith('/inbox/threads') && method === 'GET') {
        threadRequests.push(url)
        return json({ items: [thread(7, { contact_first_name: 'Listed', contact_last_name: 'Thread' })] })
      }
      return json({ error: 'unhandled' }, 404)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const searchbox = () => screen.getByRole('searchbox', { name: /search mail/i })

/** The pane's empty/error block with this title — scoped, because the search
 * box's own clear button shares the name "Clear search" with the block's. */
async function stateBlock(title: string): Promise<HTMLElement> {
  const heading = await screen.findByText(title)
  const block = heading.closest<HTMLElement>('[data-slot="empty-block"]')
  if (!block) throw new Error(`"${title}" is not inside an empty block`)
  return block
}

test('typing debounces into ONE full-text search request, synced to ?q=, and the list stops fetching', async () => {
  renderWithProviders(<InboxPage />)
  await screen.findByText('Listed Thread')
  const listRequestsBefore = threadRequests.length

  fireEvent.change(searchbox(), { target: { value: 'mee' } })
  fireEvent.change(searchbox(), { target: { value: 'meeting' } })
  // Echoed at once — the field never waits on the request.
  expect(searchbox()).toHaveValue('meeting')
  expect(searchRequests).toHaveLength(0)

  await waitFor(() => expect(router.search.q).toBe('meeting'), { timeout: 5000 })
  await screen.findByText('Jamie Lin')

  // One request, for the settled query — not one per keystroke.
  expect(searchRequests).toHaveLength(1)
  expect(lastSearch().searchParams.get('q')).toBe('meeting')
  // Starting a search pushes history (Back returns to the unsearched inbox).
  expect(router.writes.at(-1)).toMatchObject({ replace: false, search: { q: 'meeting' } })
  // The pane now shows results, not the list — and the list isn't refetched.
  expect(screen.queryByText('Listed Thread')).not.toBeInTheDocument()
  expect(threadRequests).toHaveLength(listRequestsBefore)
})

test('a shared ?q= link runs the search immediately and fills the box', async () => {
  router.search = { q: 'meeting' }
  renderWithProviders(<InboxPage />)

  await screen.findByText('Jamie Lin')
  expect(searchbox()).toHaveValue('meeting')
  expect(lastSearch().searchParams.get('q')).toBe('meeting')
  expect(threadRequests).toHaveLength(0)
})

test('a hit renders its snippet with the matched runs marked, and says which side matched', async () => {
  router.search = { q: 'meeting' }
  renderWithProviders(<InboxPage />)

  const list = await screen.findByRole('list', { name: /search results for meeting/i })
  const [first, second] = within(list).getAllByRole('listitem')
  if (!first || !second) throw new Error('expected two result rows')

  // Body match: the snippet is shown with <mark> around the matched word.
  expect(first).toHaveTextContent('Happy to set up a meeting next week.')
  expect([...first.querySelectorAll('mark')].map((m) => m.textContent)).toEqual(['meeting'])
  expect(first).toHaveTextContent('Matched in received & sent')
  // A body-only match keeps the thread's familiar subject.
  expect(within(first).getByText('Subject 0')).toBeInTheDocument()

  // Subject match: the highlighted subject replaces the plain one; the
  // snippet is our own outbound message, and says so.
  expect(second).toHaveTextContent('Our meeting notes')
  expect(within(second).queryByText('Call notes')).not.toBeInTheDocument()
  expect(second).toHaveTextContent('You: Notes from our call.')
  expect(second).toHaveTextContent('Matched in sent')
  expect(screen.getByText('2 results · all shown')).toBeInTheDocument()
})

test('the current folder, mailbox, category and reply class all ride along on the search', async () => {
  router.search = { q: 'pricing', mailbox: 'mb-2', class: 'positive', label: 'lb-1', scope: 'today' }
  renderWithProviders(<InboxPage />)
  await waitFor(() => expect(searchRequests.length).toBeGreaterThan(0))

  const params = lastSearch().searchParams
  expect(params.get('q')).toBe('pricing')
  expect(params.get('mailbox_id')).toBe('mb-2')
  expect(params.get('reply_class')).toBe('positive')
  expect(params.get('label')).toBe('lb-1')
  expect(params.get('scope')).toBe('today')
  // `today` is calendar-dependent, so the viewer's offset goes too.
  expect(params.get('tz_offset')).toBe(String(-new Date().getTimezoneOffset()))
})

test('changing folder while searching keeps the query and re-searches inside the new folder', async () => {
  router.search = { q: 'pricing' }
  renderWithProviders(<InboxPage />)
  await screen.findByText('Jamie Lin')

  fireEvent.click(screen.getByRole('button', { name: /support@acme\.test/i }))

  await waitFor(() => expect(lastSearch().searchParams.get('mailbox_id')).toBe('mb-2'))
  expect(lastSearch().searchParams.get('q')).toBe('pricing')
  expect(router.search.q).toBe('pricing')
})

test('Load more fetches the next page with next_cursor and appends it', async () => {
  respondToSearch = (url) =>
    url.searchParams.get('cursor') === 'page-2'
      ? { status: 200, body: { items: [hit(2, { threadOverrides: { contact_first_name: 'Page', contact_last_name: 'Two' } })], next_cursor: null } }
      : { status: 200, body: { items: [hit(0, { threadOverrides: { contact_first_name: 'Page', contact_last_name: 'One' } })], next_cursor: 'page-2' } }
  router.search = { q: 'meeting' }
  renderWithProviders(<InboxPage />)
  await screen.findByText('Page One')
  expect(lastSearch().searchParams.get('cursor')).toBeNull()

  fireEvent.click(screen.getByRole('button', { name: 'Load more' }))

  await screen.findByText('Page Two')
  expect(lastSearch().searchParams.get('cursor')).toBe('page-2')
  // Appended, not replaced — and the last page ends the pager.
  expect(screen.getByText('Page One')).toBeInTheDocument()
  expect(screen.getByText('2 results · all shown')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
})

test('a failed Load more keeps the loaded results and retries the page that failed', async () => {
  let failNextPage = true
  respondToSearch = (url) => {
    if (url.searchParams.get('cursor') === 'page-2') {
      if (failNextPage) return { status: 500, body: { error: 'boom' } }
      return { status: 200, body: { items: [hit(2, { threadOverrides: { contact_first_name: 'Page', contact_last_name: 'Two' } })], next_cursor: null } }
    }
    return { status: 200, body: { items: [hit(0, { threadOverrides: { contact_first_name: 'Page', contact_last_name: 'One' } })], next_cursor: 'page-2' } }
  }
  router.search = { q: 'meeting' }
  renderWithProviders(<InboxPage />)
  await screen.findByText('Page One')

  fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent('The search failed (500) — try again.')
  expect(screen.getByText('Page One')).toBeInTheDocument()

  failNextPage = false
  fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }))

  await screen.findByText('Page Two')
  expect(lastSearch().searchParams.get('cursor')).toBe('page-2')
  expect(screen.getByText('Page One')).toBeInTheDocument()
})

test('a search with no hits shows the empty state, not an error', async () => {
  respondToSearch = () => ({ status: 200, body: { items: [], next_cursor: null } })
  router.search = { q: 'zebra' }
  renderWithProviders(<InboxPage />)

  const block = await stateBlock('No threads match “zebra”')
  expect(block).toHaveTextContent('or a sender address')
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()

  fireEvent.click(within(block).getByRole('button', { name: 'Clear search' }))
  await waitFor(() => expect(router.search.q).toBeUndefined())
  expect(await screen.findByText('Listed Thread')).toBeInTheDocument()
})

test('a short query with no hits says addresses need three characters, rather than implying none match', async () => {
  respondToSearch = () => ({ status: 200, body: { items: [], next_cursor: null } })
  router.search = { q: 'ac' }
  renderWithProviders(<InboxPage />)

  const block = await stateBlock('No threads match “ac”')
  expect(block).toHaveTextContent('Sender addresses are only searched for 3 or more characters.')
})

test('an address-only hit says it matched the sender address, with an unhighlighted snippet', async () => {
  respondToSearch = () => ({
    status: 200,
    body: {
      items: [
        hit(0, {
          legs: ['contact'],
          body: [{ text: 'Thanks, talk soon.', match: false }],
          threadOverrides: { contact_first_name: 'Jo', contact_last_name: 'Acme', contact_email: 'jo@acme.com' },
        }),
      ],
      next_cursor: null,
    },
  })
  router.search = { q: 'acme.com' }
  renderWithProviders(<InboxPage />)

  const list = await screen.findByRole('list', { name: /search results for acme\.com/i })
  expect(list).toHaveTextContent('Matched in sender address')
  expect(list).toHaveTextContent('Thanks, talk soon.')
  expect(list.querySelector('mark')).toBeNull()
})

test('a query with nothing to look for (stop words, exclusions) explains how to fix it, not a generic error', async () => {
  respondToSearch = () => ({
    status: 400,
    body: { error: 'inbox: invalid input: q must contain at least one word to look for (only common words or exclusions were given)' },
  })
  router.search = { q: '-foo' }
  renderWithProviders(<InboxPage />)

  const block = await stateBlock('Add a word to search for')
  expect(block).toHaveTextContent('exclusions like “-foo” can’t be searched on their own')
  expect(within(block).queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument()
  expect(within(block).getByRole('button', { name: 'Clear search' })).toBeInTheDocument()
})

test('a too-broad search (422) asks for more words and offers no retry', async () => {
  respondToSearch = () => ({ status: 422, body: { error: 'inbox: search matches more than 10000 messages, campaign steps or contacts; add words to narrow it' } })
  router.search = { q: 'hello' }
  renderWithProviders(<InboxPage />)

  const block = await stateBlock('That search matches too much')
  expect(block).toHaveTextContent('add words to narrow it')
  expect(within(block).queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument()
})

test('a throttled search (429) names the Retry-After delay and can be retried', async () => {
  let throttled = true
  const ok = respondToSearch
  respondToSearch = (url) =>
    throttled ? { status: 429, body: { error: 'rate limited' }, headers: { 'retry-after': '12' } } : ok(url)
  router.search = { q: 'meeting' }
  renderWithProviders(<InboxPage />)

  const block = await stateBlock('Too many searches')
  expect(block).toHaveTextContent('try again in 12 seconds')

  throttled = false
  fireEvent.click(within(block).getByRole('button', { name: 'Try again' }))
  expect(await screen.findByText('Jamie Lin')).toBeInTheDocument()
})

test('a timed-out search (503) suggests a narrower query and offers a retry', async () => {
  respondToSearch = () => ({ status: 503, body: { error: 'inbox: search took too long; try a more specific query' } })
  router.search = { q: 'meeting' }
  renderWithProviders(<InboxPage />)

  const block = await stateBlock('That search took too long')
  expect(block).toHaveTextContent('a more specific search will likely work')
  expect(within(block).getByRole('button', { name: 'Try again' })).toBeInTheDocument()
})

test('a failed search offers Try again, which re-runs it', async () => {
  let fail = true
  const ok = respondToSearch
  respondToSearch = (url) => (fail ? { status: 500, body: { error: 'boom' } } : ok(url))
  router.search = { q: 'meeting' }
  renderWithProviders(<InboxPage />)

  expect(await screen.findByText("Couldn't search the inbox")).toBeInTheDocument()
  expect(screen.getByText('The search failed (500) — try again.')).toBeInTheDocument()

  fail = false
  fireEvent.click(screen.getByRole('button', { name: 'Try again' }))
  expect(await screen.findByText('Jamie Lin')).toBeInTheDocument()
})

test("a 400 shows the server's validation message and offers to edit the search, not a pointless retry", async () => {
  respondToSearch = () => ({ status: 400, body: { error: 'inbox: invalid input: q must not contain NUL' } })
  router.search = { q: 'meeting' }
  renderWithProviders(<InboxPage />)

  const block = await stateBlock("That search can't run")
  expect(block).toHaveTextContent(/q must not contain NUL/)
  expect(within(block).queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument()
  expect(within(block).getByRole('button', { name: 'Clear search' })).toBeInTheDocument()
})

test('an over-long query is explained without spending a request on a certain 400', async () => {
  router.search = { q: 'x'.repeat(257) }
  renderWithProviders(<InboxPage />)

  expect(await screen.findByText('That search is too long')).toBeInTheDocument()
  expect(searchRequests).toHaveLength(0)
})

test('Escape in the box clears the search at once and brings the list back', async () => {
  router.search = { q: 'meeting' }
  renderWithProviders(<InboxPage />)
  await screen.findByText('Jamie Lin')

  fireEvent.keyDown(searchbox(), { key: 'Escape' })

  expect(searchbox()).toHaveValue('')
  // Committed immediately rather than after the debounce, and replacing the
  // history entry rather than stacking a new one.
  expect(router.search.q).toBeUndefined()
  expect(router.writes.at(-1)?.replace).toBe(true)
  expect(await screen.findByText('Listed Thread')).toBeInTheDocument()
})

test('selecting a result opens its thread, as a list row does', async () => {
  router.search = { q: 'meeting' }
  renderWithProviders(<InboxPage />)

  fireEvent.click(await screen.findByText('Jamie Lin'))

  expect(router.lastNavigation).toEqual({ to: '/app/inbox/$threadId', params: { threadId: 't-0' } })
})
