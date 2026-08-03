import { screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { DeliverabilityReport } from '@/store/api'
import { DeliverabilityPage } from './deliverability-page'
// Importing the feature api registers this endpoint's tag wiring on the shared
// registry, exactly as the app does.
import './api'

// The page renders router <Link>s in the at-risk lists; stub them to anchors so
// the screen can be asserted without a real router.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, ...props }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

const jsonHeaders = { 'content-type': 'application/json' }

const REPORT: DeliverabilityReport = {
  score: {
    value: 74,
    confidence: 'high',
    delivered: 4_120,
    components: [
      { key: 'bounce', label: 'Bounces', penalty: 12, rate: 3.4, measured: true },
      // The shape that ships in v1: no complaint feed, so the component arrives
      // unmeasured. It must never read as 0%.
      { key: 'complaint', label: 'Complaints', penalty: 0, rate: null, measured: false },
      { key: 'spam_placement', label: 'Spam placement', penalty: 14, rate: 11.2, measured: true },
      { key: 'warmup', label: 'Warmup', penalty: 0, rate: null, measured: true, detail: 'Every warming mailbox is healthy.' },
    ],
  },
  series: [
    { date: '2026-08-18', delivered: 200, bounced: 4, complained: null, spam_placed: 6 },
    { date: '2026-08-19', delivered: 250, bounced: 23, complained: null, spam_placed: 5 },
    { date: '2026-08-20', delivered: 180, bounced: 3, complained: null, spam_placed: 0 },
  ],
  at_risk_mailboxes: [{ label: 'growth@atlas.test', reason: 'Bounce rate 11.4% over 640 delivered.' }],
  at_risk_domains: [{ label: 'atlas.test', reason: 'No DMARC record is published.' }],
}

function stubReport(response: DeliverabilityReport | { status: number }) {
  const fetchMock = vi.fn(
    async () =>
      'status' in response
        ? new Response(JSON.stringify({ error: 'boom' }), { status: response.status, headers: jsonHeaders })
        : new Response(JSON.stringify(response), { status: 200, headers: jsonHeaders }),
  )
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

/** The `<li>` for one score component, located by its stable data attribute. */
function componentRow(key: string): HTMLElement {
  const row = document.querySelector(`[data-component="${key}"]`)
  if (!(row instanceof HTMLElement)) throw new Error(`no component row ${key}`)
  return row
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('DeliverabilityPage', () => {
  test('an unmeasured component renders as not measured and is not styled as healthy', async () => {
    stubReport(REPORT)
    renderWithProviders(<DeliverabilityPage />)

    const complaints = await waitFor(() => componentRow('complaint'))
    expect(complaints).toHaveAttribute('data-measured', 'false')
    expect(within(complaints).getByText('Not measured')).toBeInTheDocument()
    // Not a clean zero, and not a percentage of any kind.
    expect(complaints.textContent).not.toContain('0.0%')
    expect(complaints.textContent).not.toMatch(/\d%/)
    expect(within(complaints).getByText(/No complaint feed is connected/)).toBeInTheDocument()
    expect(within(complaints).getByText(/not a clean complaint rate/)).toBeInTheDocument()
    // The healthy tone belongs to the measured-and-clean component, never here.
    expect(complaints.querySelector('.text-ok')).toBeNull()
    expect(componentRow('warmup').querySelector('.text-ok')).not.toBeNull()
  })

  test('a measured component shows its rate and the points it cost', async () => {
    stubReport(REPORT)
    renderWithProviders(<DeliverabilityPage />)

    const bounces = await waitFor(() => componentRow('bounce'))
    expect(within(bounces).getByText('3.4% — costing 12 points')).toBeInTheDocument()
    expect(within(bounces).getByText('−12 points')).toBeInTheDocument()
  })

  test('the score is the headline number, qualified by its sample and exclusions', async () => {
    stubReport(REPORT)
    renderWithProviders(<DeliverabilityPage />)

    const panel = await screen.findByRole('region', { name: 'Deliverability score' })
    expect(within(panel).getByText('74')).toBeInTheDocument()
    expect(within(panel).getByText(/Computed over 4,120 delivered/)).toBeInTheDocument()
    expect(within(panel).getByText(/Complaints wasn't measured/)).toBeInTheDocument()
  })

  test('a low-confidence score is visibly qualified rather than badged as clean', async () => {
    stubReport({
      ...REPORT,
      score: { ...REPORT.score, value: 96, confidence: 'low', delivered: 11 },
    })
    renderWithProviders(<DeliverabilityPage />)

    const panel = await screen.findByRole('region', { name: 'Deliverability score' })
    const figure = within(panel).getByText('96')
    // The number is shown but is not coloured as a verdict…
    expect(figure).toHaveClass('text-faint')
    expect(figure).not.toHaveClass('text-foreground')
    // …the band is replaced, not annotated…
    expect(within(panel).getByText('Provisional')).toBeInTheDocument()
    expect(within(panel).queryByText('Strong')).not.toBeInTheDocument()
    // …and a full sentence, not a badge, carries the reason.
    expect(within(panel).getByText(/11 delivered — too small a sample to be a verdict/)).toBeInTheDocument()
    expect(within(panel).getByText('Small sample')).toBeInTheDocument()
  })

  test('the per-day chart renders measured panels and a not-measured one', async () => {
    stubReport(REPORT)
    renderWithProviders(<DeliverabilityPage />)

    // Lazy chart, so wait for the chunk.
    const bounce = await screen.findByRole('region', { name: 'Bounce rate' })
    expect(within(bounce).getByText('Worst day 9.2% on 19 Aug')).toBeInTheDocument()

    const complaints = await screen.findByRole('region', { name: 'Complaint rate' })
    expect(within(complaints).getByText('Not measured')).toBeInTheDocument()
    expect(within(complaints).getByText(/not a run of clean days/)).toBeInTheDocument()
    // No plot at all for an unmeasured signal — a blank axis reads as zero.
    expect(complaints.querySelector('svg')).toBeNull()
    expect(within(bounce).getByRole('group', { name: /Bounce rate by day/ })).toBeInTheDocument()
  })

  test('every plotted day is also readable as text, so hover never gates a value', async () => {
    stubReport(REPORT)
    renderWithProviders(<DeliverabilityPage />)

    const table = await screen.findByRole('table', { name: /Deliverability signals per day/ })
    const row = within(table).getByRole('rowheader', { name: '19 Aug' }).closest('tr')
    expect(row).not.toBeNull()
    expect(within(row as HTMLElement).getByText('250')).toBeInTheDocument()
    expect(within(row as HTMLElement).getByText('9.2%')).toBeInTheDocument()
    // The unmeasured column says so in the table too.
    expect(within(row as HTMLElement).getAllByText('Not measured')).toHaveLength(1)
  })

  test('at-risk mailboxes and domains render their reason and link to the mailbox screen', async () => {
    stubReport(REPORT)
    renderWithProviders(<DeliverabilityPage />)

    const mailboxes = await screen.findByRole('region', { name: 'Mailboxes at risk' })
    const link = within(mailboxes).getByRole('link', {
      name: /growth@atlas\.test — Bounce rate 11\.4% over 640 delivered/,
    })
    expect(link).toHaveAttribute('href', '/app/mailboxes')

    const domains = screen.getByRole('region', { name: 'Domains at risk' })
    expect(within(domains).getByText('No DMARC record is published.')).toBeInTheDocument()
  })

  test('an empty at-risk list says so rather than being omitted', async () => {
    stubReport({ ...REPORT, at_risk_mailboxes: [], at_risk_domains: [] })
    renderWithProviders(<DeliverabilityPage />)

    expect(await screen.findByText('No mailbox is currently dragging the score down.')).toBeInTheDocument()
    expect(screen.getByText('No sending domain is currently dragging the score down.')).toBeInTheDocument()
  })

  test('a failed load is an error, not an empty dashboard', async () => {
    stubReport({ status: 500 })
    renderWithProviders(<DeliverabilityPage />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load deliverability (500)")
    expect(alert).toHaveTextContent('not a clean result')
    // Critically: no score panel, so there are no zeros to mistake for data.
    expect(screen.queryByRole('region', { name: 'Deliverability score' })).not.toBeInTheDocument()
    expect(screen.queryByText('0')).not.toBeInTheDocument()
  })

  test('a single day of history is not plotted as a trend', async () => {
    stubReport({ ...REPORT, series: [REPORT.series[0]!] })
    renderWithProviders(<DeliverabilityPage />)

    expect(await screen.findByText(/a single day is a number, not a trend/)).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Bounce rate' })).not.toBeInTheDocument()
  })
})
