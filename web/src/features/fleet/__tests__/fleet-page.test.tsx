import { screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { FleetProviderSignals, FleetWorker, ScheduledJob } from '../api'
import { FleetPage } from '../fleet-page'

const jsonHeaders = { 'content-type': 'application/json' }

const admin = { auth: { role: 'admin', status: 'authed' as const, activeWorkspaceId: 'w1' } }

/** Every URL the page requested, so the query arguments are assertable. */
let urls: string[]
let workersResponse: () => Response
let jobsResponse: () => Response

function signals(overrides: Partial<FleetProviderSignals> = {}): FleetProviderSignals {
  return {
    provider: 'gmail',
    operation: 'send',
    attempts: 400,
    successes: 310,
    auth_failures: 42,
    throttled: 30,
    blocked: 0,
    rejected: 18,
    ...overrides,
  }
}

function worker(overrides: Partial<FleetWorker> = {}): FleetWorker {
  return {
    worker_id: 'w-10-1-4-27',
    egress_ip: '203.0.113.7',
    id_family: 'ipv4',
    last_seen_at: new Date(Date.now() - 120_000).toISOString(),
    live: true,
    mailbox_count: 4,
    degraded_mailbox_count: 1,
    first_assigned_at: new Date(Date.now() - 86_400_000).toISOString(),
    signals: [signals()],
    ...overrides,
  }
}

function job(overrides: Partial<ScheduledJob> = {}): ScheduledJob {
  return {
    job_name: 'domain auth sweep',
    last_started_at: new Date(Date.now() - 300_000).toISOString(),
    last_finished_at: new Date(Date.now() - 299_000).toISOString(),
    last_duration_ms: 1200,
    last_outcome: 'ok',
    runs_in_window: 288,
    failures_in_window: 0,
    last_failure_at: null,
    ...overrides,
  }
}

function ok(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: jsonHeaders })
}

beforeEach(() => {
  urls = []
  workersResponse = () => ok({ workers: [worker()], window_hours: 24 })
  jobsResponse = () => ok({ jobs: [job()], window_hours: 24 })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      urls.push(url)
      if (/\/fleet\/workers/.test(url)) return workersResponse()
      if (/\/fleet\/jobs/.test(url)) return jobsResponse()
      if (/\/fleet\/mailboxes\//.test(url)) return ok({ decisions: [] })
      if (/\/mailboxes/.test(url)) return ok([])
      return new Response('{}', { status: 200, headers: jsonHeaders })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

// A member must see an honest state rather than three failed requests. The
// server is still the boundary — this is the courtesy half of the gate.
test('a member is shown the admin-only state and no request is made', async () => {
  renderWithProviders(<FleetPage />, {
    preloadedState: { auth: { role: 'member', status: 'authed', activeWorkspaceId: 'w1' } },
  })

  expect(await screen.findByText('Admins only')).toBeInTheDocument()
  expect(urls.filter((u) => u.includes('/fleet'))).toHaveLength(0)
})

// THE POINT OF THE SCREEN: a degrading worker must be readable without
// arithmetic, so every count that only means something against another is
// rendered beside it.
test('a worker shows attempts against successes and auth failures against throttles', async () => {
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  // The row is identified by its address, which leads the layout.
  const address = await screen.findByText('203.0.113.7')
  const row = address.closest('li')
  expect(row).not.toBeNull()
  const cell = within(row as HTMLElement)

  // 310 of 400 accepted, with the derived rate BESIDE the counts, not instead.
  expect(cell.getByText(/310\/400 accepted \(78%\)/)).toBeInTheDocument()
  // Auth failures carry their own count and their own rate.
  expect(cell.getByText(/42 credential refused \(11%\)/)).toBeInTheDocument()
  // And throttles, paired with them.
  expect(cell.getByText(/30 throttled/)).toBeInTheDocument()
  // Mailbox count paired with the degraded subset, so 1-of-4 cannot be read as
  // 1-of-400.
  expect(row).toHaveTextContent('4 of your mailboxes')
  expect(cell.getByText(/1 degraded \(25%\)/)).toBeInTheDocument()
})

// Auth failures are the provider-side signal these counters exist to expose.
// They must be in the rendered text, not behind a hover.
test('auth failures are rendered, not hidden in a tooltip', async () => {
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  const line = await screen.findByText(/42 credential refused/)
  expect(line).toBeVisible()
  // And they are not merely part of a combined failure total: throttles are a
  // separate, separately-labelled figure.
  expect(screen.getByText(/30 throttled/)).not.toBe(line)
})

// A recipient rejection is about the recipient. It is shown, and it is shown
// apart from every number that reflects on the worker.
test('recipient rejections are labelled as the recipient’s, not the worker’s', async () => {
  renderWithProviders(<FleetPage />, { preloadedState: admin })
  expect(await screen.findByText(/18 rejected by recipient/)).toBeInTheDocument()
})

// Silence is not success. A worker that reported nothing must say so rather
// than render a row of zeroes, which would read as the worst possible worker.
test('a worker that reported nothing says it is silence rather than showing zeroes', async () => {
  workersResponse = () => ok({ workers: [worker({ signals: [] })], window_hours: 24 })
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  expect(await screen.findByText(/silence, not success/)).toBeInTheDocument()
  expect(screen.queryByText(/accepted/)).not.toBeInTheDocument()
})

// A dead worker is the row that matters most, and it must be unmistakable.
test('a worker that stopped heartbeating is called out in words, not only in colour', async () => {
  workersResponse = () => ok({ workers: [worker({ live: false })], window_hours: 24 })
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  expect(await screen.findByText('Not responding')).toBeInTheDocument()
})

// The shared-fate caveat is the one thing about these numbers a reader can get
// badly wrong, so it is stated on the screen rather than left to documentation.
test('the page says the counts include other workspaces on the same worker', async () => {
  renderWithProviders(<FleetPage />, { preloadedState: admin })
  expect(await screen.findByText(/if other workspaces share a worker/i)).toBeInTheDocument()
})

// An empty fleet is an ordinary state; a failed read is not. Rendering the
// first for the second would tell an operator their mail is fine when nothing
// is known.
test('a failed worker read never renders as an empty fleet', async () => {
  workersResponse = () => new Response('{}', { status: 500, headers: jsonHeaders })
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  const alerts = await screen.findAllByRole('alert')
  expect(alerts.length).toBeGreaterThan(0)
  expect(screen.queryByText('No mailbox is pinned to a worker yet')).not.toBeInTheDocument()
})

test('a 403 on the fleet read names the role that is missing', async () => {
  workersResponse = () => new Response('{}', { status: 403, headers: jsonHeaders })
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  expect(await screen.findByText(/needs workspace admin/i)).toBeInTheDocument()
})

test('an empty fleet explains that the list fills itself on first send', async () => {
  workersResponse = () => ok({ workers: [], window_hours: 24 })
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  expect(await screen.findByText('No mailbox is pinned to a worker yet')).toBeInTheDocument()
})

// The heading reads the window the SERVER used, because the request value is
// clamped. Labelling from the request would eventually name a window nobody
// measured.
test('the window label comes from the response, not from what was requested', async () => {
  workersResponse = () => ok({ workers: [worker()], window_hours: 720 })
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  expect(await screen.findByText('last 720h')).toBeInTheDocument()
  // …and the request itself did ask for the page's own 24, under the
  // snake_case name the contract uses on the wire.
  await waitFor(() => expect(urls.some((u) => /\/fleet\/workers\?.*window_hours=24/.test(u))).toBe(true))
})

/* ------------------------------------------------------------ the sweeps */

test('a sweep shows failures against runs, and withholds the error text with a reason', async () => {
  jobsResponse = () =>
    ok({
      jobs: [
        job({
          last_outcome: 'error',
          runs_in_window: 288,
          failures_in_window: 12,
          last_failure_at: new Date(Date.now() - 600_000).toISOString(),
        }),
      ],
      window_hours: 24,
    })
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  expect(await screen.findByText(/12 failed of 288 runs/)).toBeInTheDocument()
  expect(screen.getByText('Failing')).toBeInTheDocument()
  // The absence of an error message is explained where an operator looks for
  // one, rather than leaving the screen looking as though it is hiding something.
  expect(screen.getByText(/can name another one’s data/)).toBeInTheDocument()
})

// The failure this ledger exists to surface: the sweep that quietly stopped.
// Its last outcome is 'ok' — whatever it was when it stopped — so a status
// derived from the outcome alone would paint it green.
test('a sweep that has not run in the window is reported as not running despite an ok last outcome', async () => {
  jobsResponse = () =>
    ok({
      jobs: [
        job({
          job_name: 'warmup sweep',
          last_outcome: 'ok',
          runs_in_window: 0,
          failures_in_window: 0,
          last_started_at: new Date(Date.now() - 96 * 3_600_000).toISOString(),
        }),
      ],
      window_hours: 24,
    })
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  const row = await screen.findByText('warmup sweep')
  const li = row.closest('li')
  expect(li).not.toBeNull()
  expect(within(li as HTMLElement).getByText('Not running')).toBeInTheDocument()
  expect(within(li as HTMLElement).queryByText('Healthy')).not.toBeInTheDocument()
})

test('a failed sweep read never renders as "no sweep has reported"', async () => {
  jobsResponse = () => new Response('{}', { status: 500, headers: jsonHeaders })
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  await screen.findByText('203.0.113.7')
  expect(screen.queryByText('No sweep has reported yet')).not.toBeInTheDocument()
})

/* --------------------------------------------------------- the decisions */

test('the decision log waits for a mailbox to be chosen rather than guessing one', async () => {
  renderWithProviders(<FleetPage />, { preloadedState: admin })

  expect(await screen.findByText(/Pick a mailbox to see why/)).toBeInTheDocument()
  // No per-mailbox read is issued until one is picked.
  expect(urls.filter((u) => /\/fleet\/mailboxes\//.test(u))).toHaveLength(0)
})
