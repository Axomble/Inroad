import { screen } from '@testing-library/react'
import { beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { CampaignResultRow, CampaignResults } from './api'
import { ResultsPanel } from './results-panel'

const jsonHeaders = { 'content-type': 'application/json' }

function row(overrides: Partial<CampaignResultRow>): CampaignResultRow {
  return {
    variant_id: null,
    label: 'A',
    is_base: true,
    weight: 1,
    sent: 1000,
    opens: 400,
    clicks: 100,
    replies: 20,
    bounces: 5,
    unsubscribes: 2,
    open_rate: 0.4,
    click_rate: 0.1,
    reply_rate: 0.02,
    bounce_rate: 0.005,
    unsub_rate: 0.002,
    ...overrides,
  }
}

function results(overrides: Partial<CampaignResults['steps'][number]> = {}): CampaignResults {
  return {
    campaign_id: 'camp-1',
    steps: [
      {
        step_order: 1,
        subject: 'quick question',
        rows: [row({}), row({ variant_id: 'var-b', label: 'B', is_base: false, replies: 60, reply_rate: 0.06 })],
        winner: 'B',
        winner_note: '',
        ...overrides,
      },
    ],
  }
}

let responder: () => Response
let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  responder = () => new Response(JSON.stringify(results()), { status: 200, headers: jsonHeaders })
  fetchMock = vi.fn(async () => responder())
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('renders one row per arm with rates beside the counts', async () => {
  renderWithProviders(<ResultsPanel campaignId="camp-1" />)

  expect(await screen.findByText('quick question')).toBeInTheDocument()
  // Both arms are listed. `getAllByText` because the winner sentence names B
  // too — the point here is the table rows, not how many times "B" appears.
  expect(screen.getAllByText('A').length).toBeGreaterThan(0)
  expect(screen.getAllByText('B').length).toBeGreaterThan(0)
  // Rates are shown as percentages so nobody has to divide in their head.
  expect(screen.getByText('(6.0%)')).toBeInTheDocument()
  expect(screen.getByText('(2.0%)')).toBeInTheDocument()
})

test('marks the winning arm and says so in words', async () => {
  renderWithProviders(<ResultsPanel campaignId="camp-1" />)

  expect(await screen.findByText('Winner')).toBeInTheDocument()
  expect(screen.getByText(/clearly ahead on reply rate/i)).toBeInTheDocument()
})

// "Too close to call" is the answer an operator most needs, and the one a blank
// space would hide.
test('shows the reason when there is no winner', async () => {
  responder = () =>
    new Response(
      JSON.stringify(results({ winner: null, winner_note: 'Too close to call — the leading variant isn’t clearly ahead.' })),
      { status: 200, headers: jsonHeaders },
    )
  renderWithProviders(<ResultsPanel campaignId="camp-1" />)

  expect(await screen.findByText(/too close to call/i)).toBeInTheDocument()
  expect(screen.queryByText('Winner')).not.toBeInTheDocument()
})

// A retired arm keeps its results, so it must stay in the table rather than
// disappear and take its numbers with it.
test('keeps a paused variant in the table, labelled', async () => {
  responder = () =>
    new Response(
      JSON.stringify(
        results({
          winner: null,
          winner_note: 'No replies on any variant yet.',
          rows: [row({}), row({ variant_id: 'var-b', label: 'B', is_base: false, weight: 0 })],
        }),
      ),
      { status: 200, headers: jsonHeaders },
    )
  renderWithProviders(<ResultsPanel campaignId="camp-1" />)

  expect(await screen.findByText('B')).toBeInTheDocument()
  expect(screen.getByText('(paused)')).toBeInTheDocument()
})

// A single-arm step is not an A/B test, so it gets no winner line at all —
// "no winner yet" there would imply a comparison is pending.
test('says nothing about winners for a single-arm step', async () => {
  responder = () =>
    new Response(JSON.stringify(results({ winner: null, winner_note: '', rows: [row({})] })), {
      status: 200,
      headers: jsonHeaders,
    })
  renderWithProviders(<ResultsPanel campaignId="camp-1" />)

  expect(await screen.findByText('quick question')).toBeInTheDocument()
  expect(screen.queryByText(/winner|too close/i)).not.toBeInTheDocument()
})

// 503 means the server has no reporting wired — retrying cannot help, so no
// retry button is offered.
test('a 503 explains rather than offering a pointless retry', async () => {
  responder = () => new Response(null, { status: 503 })
  renderWithProviders(<ResultsPanel campaignId="camp-1" />)

  expect(await screen.findByText(/reporting isn’t available/i)).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /retry/i })).not.toBeInTheDocument()
})

test('other failures offer a retry', async () => {
  responder = () => new Response(null, { status: 500 })
  renderWithProviders(<ResultsPanel campaignId="camp-1" />)

  expect(await screen.findByRole('button', { name: /retry/i })).toBeInTheDocument()
})

test('an empty campaign says results appear as it runs', async () => {
  responder = () =>
    new Response(JSON.stringify({ campaign_id: 'camp-1', steps: [] }), { status: 200, headers: jsonHeaders })
  renderWithProviders(<ResultsPanel campaignId="camp-1" />)

  expect(await screen.findByText(/nothing has sent yet/i)).toBeInTheDocument()
})
