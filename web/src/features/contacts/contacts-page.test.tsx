import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { ContactsPage } from './contacts-page'

// Contact search is the screen where being wrong is invisible: a query that
// silently doesn't reach the server, or a page that quietly resets, both look
// like "that contact isn't there". These tests drive the real URL round-trip —
// the router mock below is a working store, not a stub — and count the requests
// that actually leave.

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
    // Stable across renders, as TanStack's own `useNavigate` is — an unstable
    // one would hide effect-dependency bugs this suite is meant to catch.
    useNavigate: () => router.navigate,
  }
})

const jsonHeaders = { 'content-type': 'application/json' }

type ContactPageBody = {
  items: { id: string; email: string; first_name?: string }[]
  next_cursor: string | null
  prev_cursor: string | null
  total: number
  total_is_capped: boolean
}

function contactPage(overrides: Partial<ContactPageBody> = {}): ContactPageBody {
  return {
    items: [{ id: 'c-1', email: 'jo@acme.test', first_name: 'Jo' }],
    next_cursor: null,
    prev_cursor: null,
    total: 1,
    total_is_capped: false,
    ...overrides,
  }
}

let contactRequests: URL[]
/** Per-test responder for GET /contacts, keyed off the query it received. */
let respond: (url: URL) => Promise<Response> | Response

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: jsonHeaders })
}

function lastRequest(): URL {
  const last = contactRequests[contactRequests.length - 1]
  if (!last) throw new Error('no /contacts request was made')
  return last
}

beforeEach(() => {
  router.search = {}
  contactRequests = []
  respond = () => json(contactPage())

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const isRequest = input instanceof Request
      const href = isRequest ? input.url : typeof input === 'string' ? input : (input as URL).href
      const url = new URL(href, 'http://localhost')
      const method = (isRequest ? input.method : init?.method ?? 'GET').toUpperCase()

      if (url.pathname.endsWith('/lists')) return json([{ id: 'list-1', name: 'SaaS founders' }])
      if (url.pathname.endsWith('/contacts') && method === 'GET') {
        contactRequests.push(url)
        return respond(url)
      }
      return json({ error: 'unhandled' }, 404)
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function typeSearch(value: string) {
  fireEvent.change(screen.getByRole('searchbox', { name: /search all contacts/i }), {
    target: { value },
  })
}

test('reads the whole view out of the URL on first render', async () => {
  router.search = { q: 'acme', sort: 'email', limit: 25, list: 'list-1' }

  renderWithProviders(<ContactsPage />)

  await waitFor(() => expect(contactRequests).toHaveLength(1))
  const params = lastRequest().searchParams
  expect(params.get('q')).toBe('acme')
  expect(params.get('sort')).toBe('email')
  expect(params.get('limit')).toBe('25')
  expect(params.get('list')).toBe('list-1')
})

test('typing issues one request per pause, not one per keystroke, and lands in the URL', async () => {
  renderWithProviders(<ContactsPage />)
  await waitFor(() => expect(contactRequests).toHaveLength(1))

  for (const value of ['a', 'ac', 'acm', 'acme']) typeSearch(value)

  // Echoed immediately — the field never waits on the request.
  expect(screen.getByRole('searchbox', { name: /search all contacts/i })).toHaveValue('acme')

  // Generous timeouts: the assertion is "one request", not "within 300ms" — a
  // loaded machine firing the timer late must not read as a failure.
  await waitFor(() => expect(router.search.q).toBe('acme'), { timeout: 5000 })
  await waitFor(() => expect(contactRequests).toHaveLength(2), { timeout: 5000 })
  expect(lastRequest().searchParams.get('q')).toBe('acme')

  // Give any further debounce firing a chance to prove itself wrong.
  await new Promise((resolve) => setTimeout(resolve, 500))
  expect(contactRequests).toHaveLength(2)
})

test('a one-character query is explained rather than sent, since the API rejects it', async () => {
  renderWithProviders(<ContactsPage />)
  await waitFor(() => expect(contactRequests).toHaveLength(1))

  typeSearch('a')

  expect(await screen.findByText(/at least 2 characters/i, {}, { timeout: 5000 })).toBeInTheDocument()
  await new Promise((resolve) => setTimeout(resolve, 500))
  expect(contactRequests).toHaveLength(1)
  expect(router.search.q).toBeUndefined()
})

test('the loaded page stays on screen, marked busy, while the next one loads', async () => {
  respond = (url) =>
    url.searchParams.get('cursor')
      ? new Promise<Response>(() => {}) // never resolves: the page is mid-flight
      : json(contactPage({ items: [{ id: 'c-1', email: 'jo@acme.test' }], next_cursor: 'cur-2', total: 120 }))

  const { container } = renderWithProviders(<ContactsPage />)
  expect(await screen.findByText('jo@acme.test')).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: 'Next page' }))

  await waitFor(() => expect(container.querySelector('[aria-busy="true"]')).not.toBeNull())
  // No skeleton flash, no empty state — the rows the operator was reading are
  // still there.
  expect(screen.getByText('jo@acme.test')).toBeInTheDocument()
  expect(screen.queryByText(/No contacts/i)).not.toBeInTheDocument()
})

test('Next walks forward and Previous walks back down the cursor stack', async () => {
  respond = (url) => {
    const cursor = url.searchParams.get('cursor')
    if (!cursor) {
      return json(contactPage({ items: [{ id: 'c-1', email: 'page-one@acme.test' }], next_cursor: 'cur-2', total: 120 }))
    }
    return json(
      contactPage({
        items: [{ id: 'c-2', email: 'page-two@acme.test' }],
        next_cursor: 'cur-3',
        prev_cursor: 'cur-1',
        total: 120,
      }),
    )
  }

  renderWithProviders(<ContactsPage />)
  expect(await screen.findByText('page-one@acme.test')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled()
  expect(screen.getByText('1–1 of 120')).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: 'Next page' }))
  expect(await screen.findByText('page-two@acme.test')).toBeInTheDocument()
  expect(router.search.cursor).toBe('cur-2')
  expect(screen.getByText('51–51 of 120')).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: 'Previous page' }))
  await waitFor(() => expect(router.search.cursor).toBeUndefined())
  expect(await screen.findByText('page-one@acme.test')).toBeInTheDocument()
  expect(screen.queryByText('page-two@acme.test')).not.toBeInTheDocument()
  // Back at the floor: the stack is empty again, so Previous can't go further.
  expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled()
})

test('a capped total is rendered as a floor', async () => {
  respond = () => json(contactPage({ total: 10000, total_is_capped: true }))

  renderWithProviders(<ContactsPage />)

  expect(await screen.findByText('1–1 of 10,000+')).toBeInTheDocument()
})

test('a stale cursor recovers to the first page instead of dead-ending', async () => {
  router.search = { cursor: 'cur-from-another-sort' }
  respond = (url) =>
    url.searchParams.get('cursor')
      ? json({ error: 'bad cursor' }, 400)
      : json(contactPage({ items: [{ id: 'c-1', email: 'recovered@acme.test' }], total: 3 }))

  renderWithProviders(<ContactsPage />)

  expect(await screen.findByText('recovered@acme.test')).toBeInTheDocument()
  await waitFor(() => expect(router.search.cursor).toBeUndefined())
  expect(screen.getByText(/page link has expired/i)).toBeInTheDocument()
  // The failure is explained, not dressed up as an empty list.
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

test('a failed load is an error, never an empty list', async () => {
  respond = () => json({ error: 'boom' }, 500)

  renderWithProviders(<ContactsPage />)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/Couldn't load contacts \(500\)/)
  expect(screen.queryByText(/No contacts yet/i)).not.toBeInTheDocument()
})

test('no contacts at all and no matches for this query are different states', async () => {
  respond = () => json(contactPage({ items: [], total: 0 }))

  renderWithProviders(<ContactsPage />)

  expect(await screen.findByText('No contacts yet')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Clear search and show all contacts' })).not.toBeInTheDocument()

  typeSearch('acme')
  await waitFor(() => expect(router.search.q).toBe('acme'))

  expect(await screen.findByText('No contacts match this search')).toBeInTheDocument()
  expect(screen.getByText(/Nothing in this workspace matches "acme"/)).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: 'Clear search and show all contacts' }))
  await waitFor(() => expect(router.search.q).toBeUndefined())
  expect(await screen.findByText('No contacts yet')).toBeInTheDocument()
})

test('choosing a list scopes the query and drops the cursor from the old result set', async () => {
  router.search = { cursor: 'cur-2' }
  respond = () => json(contactPage({ next_cursor: 'cur-3', total: 120 }))

  renderWithProviders(<ContactsPage />)
  await waitFor(() => expect(contactRequests.length).toBeGreaterThan(0))

  fireEvent.click(await screen.findByRole('button', { name: 'SaaS founders' }))

  await waitFor(() => expect(lastRequest().searchParams.get('list')).toBe('list-1'))
  expect(lastRequest().searchParams.get('cursor')).toBeNull()
  expect(router.search.cursor).toBeUndefined()
})

test('changing the page size restarts at the first page', async () => {
  router.search = { cursor: 'cur-2' }
  respond = () => json(contactPage({ next_cursor: 'cur-3', total: 120 }))

  renderWithProviders(<ContactsPage />)
  await waitFor(() => expect(contactRequests.length).toBeGreaterThan(0))

  fireEvent.change(screen.getByRole('combobox', { name: 'Contacts per page' }), { target: { value: '100' } })

  await waitFor(() => expect(lastRequest().searchParams.get('limit')).toBe('100'))
  expect(lastRequest().searchParams.get('cursor')).toBeNull()
  expect(router.search.limit).toBe(100)
})
