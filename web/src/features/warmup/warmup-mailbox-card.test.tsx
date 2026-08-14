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

/**
 * The tabbed-placement metric alone. Scoped deliberately: the row also renders an
 * inbox and a spam percentage, so an assertion that "no percentage is shown" is
 * only meaningful against this one element.
 */
function tabbedText(): string {
  return document.querySelector('[data-slot="tabbed-placement"]')?.textContent ?? ''
}

// Tabs are structurally undetectable over IMAP — they do not exist as a concept
// there — so `null` means "nothing observing this mailbox could report a
// category", NOT "no tabs". A percentage here would tell an operator their SMTP
// mailbox has perfect primary placement when nothing measured it at all.
test('an undetectable tabbed rate is words, never a percentage', () => {
  renderWithProviders(
    <WarmupMailboxCard
      mailbox={mailbox}
      entry={{ ...entry, tabbed_rate_7d: null, tab_capable_sample_7d: 0 }}
    />,
  )

  expect(tabbedText()).toMatch(/not detectable — no partner could report a tab/i)
  // Not merely "0%": ANY figure here is a measurement claim nothing made.
  expect(tabbedText()).not.toMatch(/\d+(\.\d+)?\s*%/)
})

// Placement is attributed to the SENDER but recorded by the RECIPIENT's poller, so tab
// capability is the PARTNER's property, not this mailbox's. An earlier draft of this copy
// said "not detectable for this provider", which sends an operator to inspect the Gmail
// mailbox in front of them when the limitation is entirely on the peers it exchanges
// with. Same shape as preflight once blaming pending_auth for a refusal its DOMAIN
// caused: a true-sounding message pointing at the wrong subject.
test('does not blame this mailbox\'s own provider for a partner limitation', () => {
  renderWithProviders(
    <WarmupMailboxCard
      mailbox={mailbox}
      entry={{ ...entry, tabbed_rate_7d: null, tab_capable_sample_7d: 0 }}
    />,
  )

  expect(tabbedText()).not.toMatch(/this provider/i)
  expect(tabbedText()).toMatch(/partner/i)
})

// Both fields are optional in the contract, so an older server omits them
// entirely. That absence must read the same as an explicit null — the silent
// fallback that shipped once (`lane` omitted, every card read "Proving") is the
// failure this asserts against.
test('a payload with no tabbed fields at all is undetectable, not zero', () => {
  renderWithProviders(<WarmupMailboxCard mailbox={mailbox} entry={entry} />)

  expect(tabbedText()).toMatch(/not detectable — no partner could report a tab/i)
  expect(tabbedText()).not.toMatch(/\d+(\.\d+)?\s*%/)
})

// The tabbed denominator is not the observations count beside it: only readers
// that could have seen a tab contribute. Showing the rate without its own sample
// invites comparing 35% against a 40-observation inbox rate — two populations.
test('a measured tabbed rate carries its own tab-capable sample count', () => {
  renderWithProviders(
    <WarmupMailboxCard
      mailbox={mailbox}
      entry={{ ...entry, placement_sample_7d: 40, tabbed_rate_7d: 0.35, tab_capable_sample_7d: 25 }}
    />,
  )

  expect(tabbedText()).toMatch(/35%/)
  expect(tabbedText()).toMatch(/25 tab-capable/)
  expect(tabbedText()).not.toMatch(/not detectable/i)
  // The inbox/spam denominator stays its own number, unshared.
  expect(screen.getByText('40 observations')).toBeInTheDocument()
})

// The opposite case, and the one a "falsy means unknown" implementation gets
// wrong: 0 over a real tab-capable sample IS a measurement — every categorisable
// message landed in the primary inbox — and hiding it behind "not detectable"
// would throw away the only good news this metric can deliver.
test('a zero rate over real tab-capable observations renders as a measured 0%', () => {
  renderWithProviders(
    <WarmupMailboxCard
      mailbox={mailbox}
      entry={{ ...entry, tabbed_rate_7d: 0, tab_capable_sample_7d: 18 }}
    />,
  )

  expect(tabbedText()).toMatch(/\b0%/)
  expect(tabbedText()).toMatch(/18 tab-capable/)
  expect(tabbedText()).not.toMatch(/not detectable/i)
})

// A rate with no denominator behind it is a contradiction, and the safe reading
// of a contradiction is the absence — never a printed fraction of nothing.
test('a rate over zero tab-capable observations is not presented as a measurement', () => {
  renderWithProviders(
    <WarmupMailboxCard
      mailbox={mailbox}
      entry={{ ...entry, tabbed_rate_7d: 0.5, tab_capable_sample_7d: 0 }}
    />,
  )

  expect(tabbedText()).toMatch(/not detectable — no partner could report a tab/i)
  expect(tabbedText()).not.toMatch(/\d+(\.\d+)?\s*%/)
})

// Nothing reads this number — no threshold, no lane, no promotion bar (design
// §8). It has to say so in both states: beside a rate so a high one isn't read as
// the reason for a throttle, and beside the absence so an SMTP operator doesn't
// read "not detectable" as a penalty.
test('the tabbed rate states that it gates nothing, measured or not', () => {
  const { unmount } = renderWithProviders(
    <WarmupMailboxCard mailbox={mailbox} entry={{ ...entry, tabbed_rate_7d: 0.6, tab_capable_sample_7d: 30 }} />,
  )
  expect(tabbedText()).toMatch(/gates nothing/i)

  unmount()
  renderWithProviders(
    <WarmupMailboxCard mailbox={mailbox} entry={{ ...entry, tabbed_rate_7d: null, tab_capable_sample_7d: 0 }} />,
  )
  expect(tabbedText()).toMatch(/gates nothing/i)
})

// Rounding a real signal down to "0%" is the same false-clean reading in a
// smaller costume, and it applies to the gating rates too.
test('a positive rate too small to round to a whole percent is not shown as zero', () => {
  renderWithProviders(
    <WarmupMailboxCard
      mailbox={mailbox}
      entry={{ ...entry, spam_rate_7d: 0.003, tabbed_rate_7d: 0.003, tab_capable_sample_7d: 300 }}
    />,
  )

  expect(tabbedText()).toMatch(/<1%/)
  expect(tabbedText()).not.toMatch(/\b0%/)
  // One vocabulary, not two: the gating spam rate reads the same way.
  expect(screen.getAllByText('<1%')).toHaveLength(2)
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

/** The identity disclosure, reached the way a screen reader reaches it. */
function identityToggle(): HTMLElement {
  return screen.getByRole('button', { name: /sending identity for a@example\.com/i })
}

// Diagnostic detail, so it stays collapsed: the metrics line above it carries
// the figures that gate anything, and six authentication facts sitting beside
// them would read as more of the same.
test('the observed identity is collapsed until the operator opens it', () => {
  renderWithProviders(
    <WarmupMailboxCard
      mailbox={mailbox}
      entry={{
        ...entry,
        identity: {
          dkim_domain: 'acme.test',
          return_path_domain: 'acme.test',
          spf_result: 'pass',
          dkim_result: 'pass',
          dmarc_result: 'fail',
          observed_at: '2026-08-14T09:30:00Z',
        },
      }}
    />,
  )

  const toggle = identityToggle()
  expect(toggle).toHaveAttribute('aria-expanded', 'false')
  expect(screen.queryByText('DKIM signing domain')).not.toBeInTheDocument()

  fireEvent.click(toggle)

  expect(toggle).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByText('DKIM signing domain')).toBeInTheDocument()
  // The failing verdict arrives with its disclaimer, not on its own.
  expect(document.querySelector('[data-slot="warmup-identity"]')?.textContent).toMatch(
    /fail[^·]*· gates nothing/,
  )
})

// The disclosure opens whether or not there is anything behind it: a mailbox
// with no observed identity is told that, rather than being left to read a
// missing control as a missing capability.
test('a mailbox with no observed identity still opens, and says nothing was seen', () => {
  renderWithProviders(<WarmupMailboxCard mailbox={mailbox} entry={{ ...entry, identity: null }} />)

  fireEvent.click(identityToggle())

  expect(screen.getByText(/has been observed with identity facts yet/i)).toBeInTheDocument()
})

test('a mailbox that is not warming up has no identity to show', () => {
  renderWithProviders(<WarmupMailboxCard mailbox={mailbox} entry={undefined} />)

  expect(screen.queryByRole('button', { name: /sending identity/i })).not.toBeInTheDocument()
})

// Two disclosures on one row, and each has to open its own panel — an operator
// asking "what was this signed as" must not be handed the change history.
test('the identity and history disclosures are independent', () => {
  renderWithProviders(<WarmupMailboxCard mailbox={mailbox} entry={entry} />)

  fireEvent.click(identityToggle())

  expect(document.querySelector('[data-slot="warmup-identity"]')).not.toBeNull()
  expect(screen.getByRole('button', { name: /change history for a@example\.com/i })).toHaveAttribute(
    'aria-expanded',
    'false',
  )
  expect(historyRequests()).toBe(0)
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
