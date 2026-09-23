import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { AuditEvent, AuditEventList } from '@/store/api'
import { AuditLogPage } from '../audit-log-page'

// The router mock is a working URL store, not a stub: the filters write through
// `useUrlPatch` and are read back through `useSearch`, so these tests drive the
// real round-trip from a control to the request that leaves.
const router = vi.hoisted(() => {
  const listeners = new Set<() => void>()
  const state = {
    search: {} as Record<string, unknown>,
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
    useNavigate: () => router.navigate,
  }
})

const jsonHeaders = { 'content-type': 'application/json' }
const admin = { auth: { role: 'admin', status: 'authed' as const, activeWorkspaceId: 'w1' } }

function event(overrides: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 'e-1',
    action: 'campaign.paused',
    actor_type: 'user',
    actor_id: 'u-1',
    actor_user_id: 'u-1',
    actor_email: 'jo@acme.test',
    target_type: 'campaign',
    target_id: 'c-1',
    ip: '203.0.113.7',
    user_agent: 'Mozilla/5.0 (X11; Linux x86_64)',
    metadata: { name: 'Q3 outreach' },
    created_at: new Date(Date.now() - 3_600_000).toISOString(),
    ...overrides,
  }
}

function list(events: AuditEvent[], nextCursor: string | null = null): AuditEventList {
  return { events, next_cursor: nextCursor }
}

function ok(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: jsonHeaders })
}

function status(code: number, body: unknown = { error: 'nope' }): Response {
  return new Response(JSON.stringify(body), { status: code, headers: jsonHeaders })
}

/** Every audit request that left, parsed, so the query arguments are assertable. */
let requests: URL[]
let respond: (url: URL) => Response

/**
 * The rendered log. Queries are scoped to it because every action label also
 * appears as an <option> in the Action filter, and a row assertion that matched
 * the dropdown would pass with no rows on screen at all.
 */
function feed() {
  const element = document.querySelector('[data-slot="audit-feed"]')
  if (!(element instanceof HTMLElement)) throw new Error('the audit feed is not rendered')
  return within(element)
}

function auditParams(): Record<string, string>[] {
  return requests.map((url) => Object.fromEntries(url.searchParams))
}

beforeEach(() => {
  router.search = {}
  requests = []
  respond = () => ok(list([event()]))
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      const url = new URL(raw, 'http://localhost')
      if (url.pathname.endsWith('/audit-events')) {
        requests.push(url)
        return respond(url)
      }
      return ok({})
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

// The server is the boundary; this is the courtesy half. A member must see an
// honest state, and must not send a request that can only come back 403.
test('a member sees the owners-and-admins state and no request is made', async () => {
  renderWithProviders(<AuditLogPage />, { preloadedState: { auth: { role: 'member', status: 'authed' } } })

  expect(await screen.findByText('Owners and admins only')).toBeInTheDocument()
  expect(requests).toHaveLength(0)
})

test.each(['admin', 'owner'])('a %s gets rows with readable actions, actor, target and IP', async (role) => {
  const createdAt = new Date(Date.now() - 3_600_000).toISOString()
  respond = () => ok(list([event({ created_at: createdAt })]))
  renderWithProviders(<AuditLogPage />, { preloadedState: { auth: { role, status: 'authed' } } })

  const row = (await feed().findByText('Campaign paused')).closest('li') as HTMLElement
  const cell = within(row)
  expect(cell.getByText('jo@acme.test')).toBeInTheDocument()
  expect(cell.getByText('campaign · c-1')).toBeInTheDocument()
  expect(cell.getByText('203.0.113.7')).toBeInTheDocument()
  // Relative AND absolute time, with the machine-readable instant on <time>.
  expect(cell.getByText('1 hour ago')).toHaveAttribute('datetime', createdAt)
  // The first request is unfiltered and asks for a page, with no cursor.
  expect(auditParams()[0]).toEqual({ limit: '50' })
})

test('actors render by type, and an unauthenticated attempt is never attributed to an account', async () => {
  respond = () =>
    ok(
      list([
        event({
          id: 'e-failed',
          action: 'auth.login_failed',
          actor_id: null,
          actor_user_id: null,
          actor_email: null,
          target_type: null,
          target_id: null,
          metadata: { email: 'ceo@acme.test' },
        }),
        event({ id: 'e-key', action: 'apikey.created', actor_type: 'api_key', actor_id: 'key-9' }),
        event({ id: 'e-agent', action: 'campaign.started', actor_type: 'agent', actor_id: 'run-1' }),
        event({ id: 'e-sys', action: 'mailbox.paused', actor_type: 'system', actor_id: 'warmup', actor_email: null }),
      ]),
    )
  renderWithProviders(<AuditLogPage />, { preloadedState: admin })

  const failed = (await feed().findByText('Unknown (failed sign-in)')).closest('li') as HTMLElement
  expect(within(failed).getByText('Sign-in failed')).toBeInTheDocument()
  expect(within(failed).getByText('tried ceo@acme.test')).toBeInTheDocument()
  // Targetless: a dash, not an empty cell.
  expect(within(failed).getByText('—')).toBeInTheDocument()

  expect(feed().getByText('API key key-9')).toBeInTheDocument()
  expect(feed().getByText('created by jo@acme.test')).toBeInTheDocument()
  expect(feed().getByText('Agent')).toBeInTheDocument()
  expect(feed().getByText('System')).toBeInTheDocument()
  expect(feed().getByText('warmup')).toBeInTheDocument()
})

test('each filter reaches the request as the API spells it', async () => {
  renderWithProviders(<AuditLogPage />, { preloadedState: admin })
  await feed().findByText('Campaign paused')

  fireEvent.change(screen.getByLabelText('Action'), { target: { value: 'campaign' } })
  await waitFor(() => expect(auditParams().at(-1)).toEqual({ action: 'campaign', limit: '50' }))

  fireEvent.change(screen.getByLabelText('Actor'), { target: { value: 'api_key' } })
  await waitFor(() => expect(auditParams().at(-1)).toMatchObject({ action: 'campaign', actor_type: 'api_key' }))

  fireEvent.change(screen.getByLabelText('From'), { target: { value: '2026-09-01' } })
  fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-09-03' } })
  await waitFor(() =>
    expect(auditParams().at(-1)).toEqual({
      action: 'campaign',
      actor_type: 'api_key',
      since: new Date(2026, 8, 1).toISOString(),
      until: new Date(2026, 8, 4).toISOString(),
      limit: '50',
    }),
  )
  // And the URL holds the filters, so the view is linkable.
  expect(router.search).toEqual({ action: 'campaign', actor: 'api_key', from: '2026-09-01', to: '2026-09-03' })
})

test('filters are read from the URL on arrival', async () => {
  router.search = { action: 'auth.login_failed', actor: 'user', junk: 'x' }
  renderWithProviders(<AuditLogPage />, { preloadedState: admin })

  await waitFor(() => expect(auditParams()[0]).toEqual({ action: 'auth.login_failed', actor_type: 'user', limit: '50' }))
  expect(screen.getByLabelText('Action')).toHaveValue('auth.login_failed')
})

test('load more appends the next page by cursor and ends with an end-of-log marker', async () => {
  respond = (url) =>
    url.searchParams.get('cursor') === 'c2'
      ? ok(list([event({ id: 'e-2', action: 'mailbox.connected' })]))
      : ok(list([event()], 'c2'))
  renderWithProviders(<AuditLogPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: 'Load older events' }))

  expect(await feed().findByText('Mailbox connected')).toBeInTheDocument()
  // The first page is still on screen: this appends, it does not page away.
  expect(feed().getByText('Campaign paused')).toBeInTheDocument()
  expect(auditParams().at(-1)).toEqual({ limit: '50', cursor: 'c2' })
  expect(feed().getByText('That is everything for these filters.')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Load older events' })).not.toBeInTheDocument()
})

// A cursor is bound to the filters that minted it; carrying one across a filter
// change would be refused, and dropping it silently would splice two lists.
test('changing a filter drops the loaded pages and the cursor', async () => {
  respond = (url) => {
    if (url.searchParams.get('cursor') === 'c2') return ok(list([event({ id: 'e-2', action: 'mailbox.connected' })]))
    if (url.searchParams.get('action') === 'auth') return ok(list([event({ id: 'e-3', action: 'auth.login' })]))
    return ok(list([event()], 'c2'))
  }
  renderWithProviders(<AuditLogPage />, { preloadedState: admin })
  fireEvent.click(await screen.findByRole('button', { name: 'Load older events' }))
  await feed().findByText('Mailbox connected')

  fireEvent.change(screen.getByLabelText('Action'), { target: { value: 'auth' } })

  expect(await feed().findByText('Signed in')).toBeInTheDocument()
  expect(feed().queryByText('Mailbox connected')).not.toBeInTheDocument()
  expect(feed().queryByText('Campaign paused')).not.toBeInTheDocument()
  expect(auditParams().at(-1)).toEqual({ action: 'auth', limit: '50' })
  expect(auditParams().filter((p) => p.action === 'auth' && p.cursor !== undefined)).toHaveLength(0)
})

test('expanding a row shows metadata and the user agent — as text, never markup', async () => {
  const hostile = '<img src=x onerror="alert(1)">'
  respond = () =>
    ok(
      list([
        event({
          action: 'member.role_changed',
          metadata: { to_role: 'admin', from_role: 'member', note: hostile },
          user_agent: `<script>alert(2)</script> Mozilla/5.0`,
        }),
      ]),
    )
  const { container } = renderWithProviders(<AuditLogPage />, { preloadedState: admin })

  const toggle = (await feed().findByText('Role changed')).closest('button') as HTMLElement
  expect(toggle).toHaveAttribute('aria-expanded', 'false')
  expect(feed().queryByText('from_role')).not.toBeInTheDocument()

  fireEvent.click(toggle)

  expect(toggle).toHaveAttribute('aria-expanded', 'true')
  const details = container.querySelector('[data-slot="audit-event-details"]') as HTMLElement
  expect(toggle).toHaveAttribute('aria-controls', details.id)
  const d = within(details)
  expect(d.getByText('from_role')).toBeInTheDocument()
  expect(d.getByText('member')).toBeInTheDocument()
  expect(d.getByText(hostile)).toBeInTheDocument()
  expect(d.getByText('<script>alert(2)</script> Mozilla/5.0')).toBeInTheDocument()
  // The hostile strings stayed strings: no element was created from either.
  expect(container.querySelector('img, script')).toBeNull()
  expect(container.querySelector('a')).toBeNull()

  fireEvent.click(toggle)
  expect(feed().queryByText('from_role')).not.toBeInTheDocument()
})

test('a 403 that arrives anyway is explained, with no empty list beneath it', async () => {
  respond = () => status(403, { error: 'forbidden' })
  renderWithProviders(<AuditLogPage />, { preloadedState: admin })

  expect(await screen.findByRole('alert')).toHaveTextContent(/owners and admins only/)
  expect(feed().queryByText('Nothing has been recorded yet')).not.toBeInTheDocument()
})

test('a failed load offers a retry that recovers', async () => {
  let fail = true
  respond = () => (fail ? status(500, { error: 'could not list audit events' }) : ok(list([event()])))
  renderWithProviders(<AuditLogPage />, { preloadedState: admin })

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent('could not list audit events')
  fail = false
  fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }))

  expect(await feed().findByText('Campaign paused')).toBeInTheDocument()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

test('an empty log and an empty filter say different things', async () => {
  respond = () => ok(list([]))
  renderWithProviders(<AuditLogPage />, { preloadedState: admin })
  expect(await feed().findByText('Nothing has been recorded yet')).toBeInTheDocument()

  fireEvent.change(screen.getByLabelText('Actor'), { target: { value: 'system' } })
  expect(await feed().findByText('No events match these filters')).toBeInTheDocument()

  // The empty state's own way out clears every filter.
  const clears = screen.getAllByRole('button', { name: 'Clear filters' })
  fireEvent.click(clears[clears.length - 1] as HTMLElement)
  expect(await feed().findByText('Nothing has been recorded yet')).toBeInTheDocument()
  expect(router.search).toEqual({})
})

test('a backwards date range is caught before it is sent', async () => {
  router.search = { from: '2026-09-05', to: '2026-09-01' }
  renderWithProviders(<AuditLogPage />, { preloadedState: admin })

  expect(await screen.findByRole('alert')).toHaveTextContent(/end date is before the start date/)
  expect(requests).toHaveLength(0)
})

test('refresh refetches the first page and drops the older ones', async () => {
  respond = (url) =>
    url.searchParams.get('cursor') === 'c2'
      ? ok(list([event({ id: 'e-2', action: 'mailbox.connected' })]))
      : ok(list([event()], 'c2'))
  renderWithProviders(<AuditLogPage />, { preloadedState: admin })
  fireEvent.click(await screen.findByRole('button', { name: 'Load older events' }))
  await feed().findByText('Mailbox connected')
  const before = requests.length

  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))

  await waitFor(() => expect(requests.length).toBe(before + 1))
  expect(auditParams().at(-1)).toEqual({ limit: '50' })
  expect(feed().queryByText('Mailbox connected')).not.toBeInTheDocument()
  expect(feed().getByText('Campaign paused')).toBeInTheDocument()
})
