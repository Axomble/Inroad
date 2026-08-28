import { screen } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { WarmupPage } from '../warmup-page'

const jsonHeaders = { 'content-type': 'application/json' }

let overviewResponder: () => Response
let mailboxesResponder: () => Response

beforeEach(() => {
  overviewResponder = () =>
    new Response(JSON.stringify({ pool_size: 1, active: false, mailboxes: [] }), { status: 200, headers: jsonHeaders })
  mailboxesResponder = () =>
    new Response(
      JSON.stringify([
        { id: 'mb-1', email: 'a@example.com', provider: 'gmail', status: 'active', daily_cap: 50 },
        { id: 'mb-2', email: 'b@example.com', provider: 'gmail', status: 'active', daily_cap: 50 },
      ]),
      { status: 200, headers: jsonHeaders },
    )

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
      if (url.includes('/warmup/overview')) return overviewResponder()
      return mailboxesResponder()
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('shows the "needs at least 2 mailboxes" notice when the pool is inactive', async () => {
  renderWithProviders(<WarmupPage />)

  expect(await screen.findByText(/warmup needs at least 2 mailboxes/i)).toBeInTheDocument()
  // The pool-size summary reflects the single enrolled participant and idle state.
  expect(screen.getByText(/idle — needs 2\+/i)).toBeInTheDocument()
})

test('does not show the inactive notice once the pool is active', async () => {
  overviewResponder = () =>
    new Response(
      JSON.stringify({
        pool_size: 2,
        active: true,
        mailboxes: [
          { mailbox_id: 'mb-1', email: 'a@example.com', enabled: true, health_state: 'healthy', health_reason: '', today_sent: 2, today_target: 4, inbox_rate_7d: 0.9, spam_rate_7d: 0.1 },
        ],
      }),
      { status: 200, headers: jsonHeaders },
    )

  renderWithProviders(<WarmupPage />)

  expect(await screen.findByText(/exchanging mail/i)).toBeInTheDocument()
  expect(screen.queryByText(/warmup needs at least 2 mailboxes/i)).not.toBeInTheDocument()
})

test('surfaces an error banner when the overview request fails', async () => {
  overviewResponder = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<WarmupPage />)

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/couldn't load the warmup overview/i)
})

test('does not present fabricated zero stats alongside the overview error', async () => {
  overviewResponder = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<WarmupPage />)

  await screen.findByRole('alert')
  // The "0 / Idle — needs 2+" fallbacks would be misleading next to "couldn't
  // load"; the strip shows em-dashes for unknown values instead.
  expect(screen.queryByText(/idle — needs 2\+/i)).not.toBeInTheDocument()
  expect(screen.queryByText(/exchanging mail/i)).not.toBeInTheDocument()
  expect(screen.getAllByText('—').length).toBeGreaterThan(0)
})

/* --------------------------------------------------- correlated degradation */

/** An overview whose two degraded participants share one signing domain. */
function overviewWithIncident() {
  return {
    pool_size: 2,
    active: true,
    // The floor the API serves. Without it the panel reads as "no search was
    // reported" and renders nothing, which is the honest response to a payload
    // that never said what the server was able to look at.
    incidents_min_pool: 4,
    mailboxes: [
      { mailbox_id: 'mb-1', email: 'a@example.com', enabled: true, health_state: 'paused', health_reason: 'spam rate', lane: 'quarantine', lane_reason: '', today_sent: 0, today_target: 0, placement_sample_7d: 40, inbox_rate_7d: 0.4, spam_rate_7d: 0.6 },
      { mailbox_id: 'mb-2', email: 'b@example.com', enabled: true, health_state: 'paused', health_reason: 'spam rate', lane: 'quarantine', lane_reason: '', today_sent: 0, today_target: 0, placement_sample_7d: 40, inbox_rate_7d: 0.4, spam_rate_7d: 0.6 },
    ],
    incidents: [
      {
        dimension: 'signing_domain',
        value: 'mail.acme.test',
        member_mailbox_ids: ['mb-1', 'mb-2'],
        cohort_size: 3,
        degraded_inside: 2,
        cohort_outside: 8,
        degraded_outside: 1,
        lift: 5.3,
      },
    ],
  }
}

// An incident is a statement about SEVERAL mailboxes, so it cannot live in one
// mailbox's disclosure: buried there it is four identical panels an operator has
// to open and diff by hand, which is the work it exists to remove.
test('a correlated incident is reported on the pool, above the mailbox list', async () => {
  overviewResponder = () =>
    new Response(JSON.stringify(overviewWithIncident()), { status: 200, headers: jsonHeaders })

  renderWithProviders(<WarmupPage />)

  const panel = await screen.findByRole('region', { name: /correlated degradation/i })
  expect(panel).toHaveTextContent('mail.acme.test')
  // The arithmetic, not a verdict: both counts and the concentration.
  expect([...panel.querySelectorAll('[data-slot="incident-stat"]')].map((n) => n.textContent)).toEqual([
    '2 of 3',
    '1 of 8',
    '5.3×',
  ])

  // Immediately before the mailbox list, not after it and not inside a card: an
  // operator who reads this after the cards has already diffed them by hand.
  const list = panel.nextElementSibling
  expect(list?.tagName).toBe('UL')
  expect(list).toHaveTextContent('a@example.com')
  expect(list).toHaveTextContent('b@example.com')
})

/* ------------------------------------------------------- observer trust */

// The overview's own `discounted_observers` has to reach a panel: nothing else in
// the system reads the field (security.md invariant 59), so an unwired payload is
// a feature that exists on the wire and nowhere else.
test('a published observer verdict is reported on the pool, above the mailbox list', async () => {
  overviewResponder = () =>
    new Response(
      JSON.stringify({
        ...overviewWithIncident(),
        discounted_observers: [
          {
            observer_mailbox_id: 'mb-2',
            cohort: 'microsoft',
            spam: 59,
            total: 130,
            spam_rate: 0.45,
            cohort_spam_rate: 0.12,
            lift: 3.75,
          },
        ],
      }),
      { status: 200, headers: jsonHeaders },
    )

  renderWithProviders(<WarmupPage />)

  const panel = await screen.findByRole('region', { name: /spam reporting outliers/i })
  // Named by its email from the same payload, with the arithmetic beside it.
  expect(panel.querySelector('[data-slot="observer-mailbox"]')?.textContent).toBe('b@example.com')
  expect([...panel.querySelectorAll('[data-slot="observer-stat"]')].map((n) => n.textContent)).toEqual([
    '59 of 130 (45%)',
    '12%',
    '3.8×',
  ])

  // Ahead of the correlation panel, which stays adjacent to the list it names
  // members of — this one qualifies the evidence both of them rest on.
  expect(panel.nextElementSibling).toHaveAttribute('data-slot', 'warmup-incidents')
})

// There is deliberately no test here for "the panel claims nothing when the
// overview failed to load". On that path `overview` is undefined, so the
// incidents array and the pool are BOTH absent, and the panel cannot render
// whichever of the two a future change breaks — an assertion that cannot
// distinguish its property from a sibling fact proves nothing. The property that
// can be isolated is a server that answers with a pool but no incidents, and
// warmup-incidents-panel.test.tsx tests it there.

/* ---------------------------------------------------------- sentinels */

// The pool-level facts have to reach a panel: an operator cannot read
// "sentinel-corroborated" off a card without somewhere that says what the pool's
// measurement arrangement is, and how much of the pool is now doing the measuring.
test('the sentinel pool facts are reported above the mailbox list', async () => {
  const base = overviewWithIncident()
  const [designated, ordinary] = base.mailboxes
  overviewResponder = () =>
    new Response(
      JSON.stringify({
        ...base,
        sentinel_count: 1,
        sentinel_pool_oversized: false,
        sentinel_pool_share: 0.5,
        mailboxes: [
          { ...designated, is_sentinel: true },
          { ...ordinary, is_sentinel: false },
        ],
      }),
      { status: 200, headers: jsonHeaders },
    )

  renderWithProviders(<WarmupPage />)

  const panel = await screen.findByRole('region', { name: /measurement sentinels/i })
  expect(panel.querySelector('[data-slot="sentinel-mailbox"]')?.textContent).toBe('a@example.com')
  expect(panel).toHaveTextContent(/1 of 2 mailboxes/)
  // Nothing is enforced, so nothing here is an alert.
  expect(panel.querySelector('[data-slot="sentinel-advisory"]')).toBeNull()
})

// An overview that says nothing about sentinels must not produce an empty state:
// the field is absent on a build that does not report them, and "no sentinel is
// designated" would describe a pool the server never described.
test('an overview that never mentions sentinels draws no sentinel panel', async () => {
  overviewResponder = () =>
    new Response(JSON.stringify(overviewWithIncident()), { status: 200, headers: jsonHeaders })

  renderWithProviders(<WarmupPage />)

  await screen.findByRole('region', { name: /correlated degradation/i })
  expect(screen.queryByRole('region', { name: /measurement sentinels/i })).toBeNull()
})

// The wiring assertion, and the reason it exists: `sentinel_count` was declared,
// generated, read by the UI and never actually sent, so the undefined branch
// rendered permanently and every unit test still passed. A panel is only shipped
// once something proves the page mounts it against a real payload.
test('a published content-version split reaches the page', async () => {
  overviewResponder = () =>
    new Response(
      JSON.stringify({
        pool_size: 2,
        active: true,
        mailboxes: [],
        content_versions: [
          { version: 'sl1:aaaaaaaaaaaaaaaa', inbox: 40, spam: 10, placement_sample: 50, inbox_rate: 0.8, spam_rate: 0.2 },
          { version: 'sl1:bbbbbbbbbbbbbbbb', inbox: 8, spam: 2, placement_sample: 10, inbox_rate: null, spam_rate: null },
        ],
      }),
      { status: 200, headers: jsonHeaders },
    )

  renderWithProviders(<WarmupPage />)

  const panel = await screen.findByRole('region', { name: /placement by template/i })
  expect(panel).toHaveTextContent('sl1:aaaaaaaa…')
  expect(panel).toHaveTextContent('80%')
  // The thin row keeps its evidence and states no rate — a 0% here would be the
  // false-clean reading the whole panel is built to avoid.
  expect(panel).toHaveTextContent(/8 inbox, 2 spam over 10 observations/)
  expect(panel).toHaveTextContent(/Not established/)
})

// The counterpart: absent means a server that does not report the split, and
// "nothing observed yet" would describe a window nobody measured.
test('an overview that never mentions templates draws no template panel', async () => {
  renderWithProviders(<WarmupPage />)

  await screen.findByText(/warmup needs at least 2 mailboxes/i)
  expect(screen.queryByRole('region', { name: /placement by template/i })).toBeNull()
})

test('shows the no-mailboxes empty state when there are none to warm', async () => {
  mailboxesResponder = () => new Response(JSON.stringify([]), { status: 200, headers: jsonHeaders })

  renderWithProviders(<WarmupPage />)

  expect(await screen.findByText(/no mailboxes to warm/i)).toBeInTheDocument()
})
