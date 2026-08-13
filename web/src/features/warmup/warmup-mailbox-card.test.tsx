import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { Mailbox, WarmupMailbox } from '@/store/api'
import { WarmupMailboxCard } from './warmup-mailbox-card'

const jsonHeaders = { 'content-type': 'application/json' }

const mailbox: Mailbox = {
  id: 'mb-1',
  email: 'a@example.com',
  provider: 'gmail',
  status: 'active',
  daily_cap: 50,
}

const entry: WarmupMailbox = {
  mailbox_id: 'mb-1',
  email: 'a@example.com',
  enabled: true,
  health_state: 'healthy',
  health_reason: '',
  lane: 'healthy',
  lane_reason: '',
  today_sent: 2,
  today_target: 4,
  placement_sample_7d: 10,
  inbox_rate_7d: 0.9,
  spam_rate_7d: 0.1,
}

// Enrolled cards fetch detail (GET) for the sparkline/settings prefill and issue
// a DELETE to disable; steer each by method so we can fail only the disable call.
let disableResponder: () => Response

/** How many times the transition-history endpoint has been asked for. */
function historyRequests(): number {
  return vi.mocked(fetch).mock.calls.filter(([input]) => {
    const url = input instanceof Request ? input.url : String(input)
    return url.includes('/transitions')
  }).length
}

beforeEach(() => {
  disableResponder = () => new Response(null, { status: 204, headers: jsonHeaders })

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      // fetchBaseQuery passes a Request object, so the method lives there — fall
      // back to init for direct (url, init) calls.
      const method = (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase()
      if (method === 'DELETE') return disableResponder()
      const url = input instanceof Request ? input.url : String(input)
      if (url.includes('/transitions')) {
        return new Response(JSON.stringify({ transitions: [] }), { status: 200, headers: jsonHeaders })
      }
      // Detail GET — minimal payload; series is short so the sparkline shows its
      // "not enough history" fallback rather than a chart.
      return new Response(
        JSON.stringify({
          participant: {
            mailbox_id: 'mb-1',
            enabled: true,
            start_volume: 4,
            max_volume: 40,
            ramp_increment: 2,
            reply_rate: 0.3,
            health_state: 'healthy',
            health_reason: '',
            lane: 'healthy',
            started_at: '2026-07-26T00:00:00Z',
            today_sent: 2,
            today_target: 4,
          },
          series: [],
        }),
        { status: 200, headers: jsonHeaders },
      )
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('an empty placement window is shown as not measured', () => {
  renderWithProviders(
    <WarmupMailboxCard
      mailbox={mailbox}
      entry={{
        ...entry,
        health_state: 'unknown',
        lane: 'probation',
        placement_sample_7d: 0,
        inbox_rate_7d: null,
        spam_rate_7d: null,
      }}
    />,
  )

  expect(screen.getAllByText('Not measured')).toHaveLength(2)
  expect(screen.getByText('0 observations')).toBeInTheDocument()
  expect(screen.getByText('Needs evidence')).toBeInTheDocument()
})

/** The two axis chips as an operator reads them: [reputation, pool]. */
function badgeText(): { health: string | undefined; lane: string | undefined } {
  return {
    health: document.querySelector('[data-slot="health-badge"]')?.textContent ?? undefined,
    lane: document.querySelector('[data-slot="lane-badge"]')?.textContent ?? undefined,
  }
}

// Lane and health are independent axes: this mailbox's outbound mail measures
// clean, and it is still not in the healthy pool. Both facts must be on the row,
// as two separately-labelled chips — one badge would have to lie about one axis.
test('a reputation-healthy mailbox still in probation shows both axes', () => {
  renderWithProviders(
    <WarmupMailboxCard
      mailbox={mailbox}
      entry={{ ...entry, health_state: 'healthy', lane: 'probation', lane_reason: '3 of 20 placement samples' }}
    />,
  )

  const badges = badgeText()
  expect(badges.health).toContain('Healthy')
  expect(badges.lane).toContain('Proving')
  // The lane chip names its axis in text, so the pair can't be read as one status.
  expect(badges.lane).toContain('Pool')
  expect(badges.health).not.toContain('Pool')
  expect(screen.getByText('3 of 20 placement samples')).toBeInTheDocument()
})

// The inverse pairing, which the single Phase 0 badge could not express: the last
// measured reputation was fine, and the mailbox is withheld from the pool anyway.
test('a quarantined mailbox reads as withheld and is distinguishable from a probation one', () => {
  const { unmount } = renderWithProviders(
    <WarmupMailboxCard
      mailbox={mailbox}
      entry={{
        ...entry,
        health_state: 'healthy',
        lane: 'quarantine',
        lane_reason: 'Withheld after a 6% hard-bounce rate; needs a clean 7-day window',
      }}
    />,
  )

  const quarantined = badgeText()
  expect(quarantined.lane).toContain('Withheld')
  expect(quarantined.lane).not.toContain('Proving')
  // Reputation is unchanged by the lane — the badges disagree, on purpose.
  expect(quarantined.health).toContain('Healthy')
  // Withheld carries the danger tone; proving carries the warmup "heat" tone.
  expect(document.querySelector('[data-slot="lane-badge"]')?.className).toContain('text-danger')
  expect(screen.getByText(/needs a clean 7-day window/i)).toBeInTheDocument()

  unmount()
  renderWithProviders(
    <WarmupMailboxCard mailbox={mailbox} entry={{ ...entry, lane: 'probation', lane_reason: 'Gathering evidence' }} />,
  )
  const probation = badgeText()
  expect(probation.lane).toContain('Proving')
  expect(probation.lane).not.toBe(quarantined.lane)
  expect(document.querySelector('[data-slot="lane-badge"]')?.className).not.toContain('text-danger')
})

// A lane this build has never heard of (or a server that stopped sending one)
// must not present the mailbox as a member of the healthy pool.
test('an unrecognized lane renders as proving, not as the healthy pool', () => {
  renderWithProviders(
    // The generated union is closed; the JSON boundary is not, so a newer server
    // value is exactly what this asserts against.
    <WarmupMailboxCard mailbox={mailbox} entry={{ ...entry, lane: 'sentinel' as WarmupMailbox['lane'] }} />,
  )

  const badges = badgeText()
  expect(badges.lane).toContain('Proving')
  expect(badges.lane).not.toContain('Healthy')
})

test('an empty lane reason renders no explanation line', () => {
  renderWithProviders(<WarmupMailboxCard mailbox={mailbox} entry={{ ...entry, lane_reason: '' }} />)

  expect(screen.queryByText(/pool status/i)).not.toBeInTheDocument()
})

// The history is per-mailbox and lives behind a disclosure on the mailbox's own
// row, so a page of ten mailboxes issues no history requests until one is opened.
test('the change history is not fetched until the operator opens it', async () => {
  renderWithProviders(<WarmupMailboxCard mailbox={mailbox} entry={entry} />)

  const toggle = screen.getByRole('button', { name: /change history for a@example\.com/i })
  expect(toggle).toHaveAttribute('aria-expanded', 'false')
  expect(historyRequests()).toBe(0)

  fireEvent.click(toggle)

  expect(toggle).toHaveAttribute('aria-expanded', 'true')
  await waitFor(() => expect(historyRequests()).toBe(1))
  expect(await screen.findByText(/nothing has happened yet/i)).toBeInTheDocument()
})

test('a mailbox that is not warming up has no history to offer', () => {
  renderWithProviders(<WarmupMailboxCard mailbox={mailbox} entry={undefined} />)

  expect(screen.queryByRole('button', { name: /change history/i })).not.toBeInTheDocument()
})

test('a failed disable surfaces the inline error alert with the generic copy', async () => {
  disableResponder = () =>
    new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<WarmupMailboxCard mailbox={mailbox} entry={entry} />)

  fireEvent.click(screen.getByRole('button', { name: /^disable$/i }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/couldn't disable warmup\. please try again\./i)
})

test('a 404 disable explains the mailbox is no longer a participant', async () => {
  disableResponder = () =>
    new Response(JSON.stringify({ error: 'not found' }), { status: 404, headers: jsonHeaders })

  renderWithProviders(<WarmupMailboxCard mailbox={mailbox} entry={entry} />)

  fireEvent.click(screen.getByRole('button', { name: /^disable$/i }))

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent(/no longer a warmup participant/i)
})
