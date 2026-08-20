import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { CampaignDeliverability, CampaignPauseEvent } from '@/store/api'
import { GuardrailsCard } from '../guardrails-card'
// Registers the Guardrails tag wiring, so a save invalidates the card's query.
import '../api'

const jsonHeaders = { 'content-type': 'application/json' }

const OK: CampaignDeliverability = {
  verdict: 'ok',
  guardrails: { auto_pause_enabled: true, bounce_pause_pct: 8, complaint_pause_pct: 1.5 },
  pause_events: [],
  score: { value: 91, confidence: 'high', delivered: 2_400, components: [] },
}

const PAUSE: CampaignPauseEvent = {
  reason: 'bounce_spike',
  metric: 'bounce_rate',
  value: 9.2,
  threshold: 8,
  delivered: 218,
  created_at: '2026-08-12T04:11:00Z',
}

/**
 * Stubs `GET /campaigns/{id}/deliverability` with each entry of `pages` in turn
 * (the last repeating) and `PUT …/guardrails` with an echo of the body. Sequencing
 * the GET is what lets a save round-trip assert the card actually re-rendered.
 */
function stubCard({ pages, putStatus = 200 }: { pages: CampaignDeliverability[] | { status: number }; putStatus?: number }) {
  let getCall = 0
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const req = input as Request
    const json = (body: unknown, status = 200) =>
      new Response(JSON.stringify(body), { status, headers: jsonHeaders })

    if (req.method === 'PUT') {
      if (putStatus !== 200) return json({ error: 'rejected' }, putStatus)
      return json(await req.clone().json())
    }
    if (!Array.isArray(pages)) return json({ error: 'boom' }, pages.status)
    const page = pages[Math.min(getCall, pages.length - 1)]
    getCall += 1
    return json(page)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

const putBodies = async (fetchMock: ReturnType<typeof vi.fn>) =>
  Promise.all(
    fetchMock.mock.calls
      .map((call) => call[0] as Request)
      .filter((req) => req.method === 'PUT')
      .map((req) => req.clone().json()),
  )

const putCount = (fetchMock: ReturnType<typeof vi.fn>) =>
  fetchMock.mock.calls.map((call) => call[0] as Request).filter((req) => req.method === 'PUT').length

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('GuardrailsCard', () => {
  test('renders both thresholds, the armed toggle, and the verdict', async () => {
    stubCard({ pages: [OK] })
    renderWithProviders(<GuardrailsCard campaignId="c1" />)

    expect(await screen.findByLabelText('Bounce threshold')).toHaveValue(8)
    expect(screen.getByLabelText('Complaint threshold')).toHaveValue(1.5)
    expect(screen.getByRole('switch', { name: 'Turn automatic pausing off' })).toHaveAttribute(
      'aria-checked',
      'true',
    )
    expect(screen.getByText('Auto-pause on')).toBeInTheDocument()
    expect(screen.getByText('Within limits')).toBeInTheDocument()
    expect(screen.getByText('This campaign has never been paused automatically.')).toBeInTheDocument()
  })

  test('an automatic pause renders its reason, rate, threshold and sample — never a bare "paused"', async () => {
    stubCard({ pages: [{ ...OK, verdict: 'paused', pause_events: [PAUSE] }] })
    renderWithProviders(<GuardrailsCard campaignId="c1" />)

    expect(await screen.findByText('Bounce spike')).toBeInTheDocument()
    const sentence = screen.getByText(
      'Paused automatically on 12 Aug — bounce rate 9.2% over 218 delivered, threshold 8.0%.',
    )
    expect(sentence).toBeInTheDocument()
    expect(screen.getByText('Automatic pauses (1)')).toBeInTheDocument()
    // The verdict itself explains where to look rather than just saying "paused".
    expect(screen.getByText('Paused by the guardrail')).toBeInTheDocument()
  })

  test('every pause is listed, not just the latest', async () => {
    const older: CampaignPauseEvent = {
      ...PAUSE,
      reason: 'complaint_spike',
      metric: 'complaint_rate',
      value: 1.9,
      threshold: 1.5,
      delivered: 900,
      created_at: '2026-07-30T10:00:00Z',
    }
    stubCard({ pages: [{ ...OK, verdict: 'paused', pause_events: [PAUSE, older] }] })
    renderWithProviders(<GuardrailsCard campaignId="c1" />)

    expect(await screen.findByText('Automatic pauses (2)')).toBeInTheDocument()
    expect(screen.getByText(/complaint rate 1\.9% over 900 delivered, threshold 1\.5%/)).toBeInTheDocument()
    expect(screen.getByText('Complaint spike')).toBeInTheDocument()
  })

  test('a warn verdict is visibly distinct from both ok and paused', async () => {
    stubCard({ pages: [{ ...OK, verdict: 'warn' }] })
    const { unmount } = renderWithProviders(<GuardrailsCard campaignId="c1" />)

    expect(await screen.findByText('Trending toward a pause')).toBeInTheDocument()
    const warnBlock = document.querySelector('[data-verdict="warn"]')
    expect(warnBlock).not.toBeNull()
    expect(warnBlock).toHaveClass('bg-warn/10')
    // The state that still allows action says so, in words as well as colour.
    expect(screen.getByText('Act now')).toBeInTheDocument()
    expect(screen.getByText(/Nothing has stopped yet/)).toBeInTheDocument()
    expect(screen.queryByText('Within limits')).not.toBeInTheDocument()
    expect(screen.queryByText('Paused by the guardrail')).not.toBeInTheDocument()
    unmount()

    // …and neither neighbour borrows its treatment.
    vi.unstubAllGlobals()
    stubCard({ pages: [{ ...OK, verdict: 'paused', pause_events: [PAUSE] }] })
    renderWithProviders(<GuardrailsCard campaignId="c2" />)
    expect(await screen.findByText('Paused by the guardrail')).toBeInTheDocument()
    expect(document.querySelector('[data-verdict="paused"]')).toHaveClass('bg-danger/10')
    expect(screen.queryByText('Act now')).not.toBeInTheDocument()
  })

  test('the toggle round-trips, sending the whole object and re-reading the card', async () => {
    const fetchMock = stubCard({
      pages: [OK, { ...OK, guardrails: { ...OK.guardrails, auto_pause_enabled: false } }],
    })
    renderWithProviders(<GuardrailsCard campaignId="c1" />)

    fireEvent.click(await screen.findByRole('switch', { name: 'Turn automatic pausing off' }))

    await waitFor(() => expect(screen.getByText('Auto-pause off')).toBeInTheDocument())
    expect(screen.getByRole('switch', { name: 'Turn automatic pausing on' })).toHaveAttribute(
      'aria-checked',
      'false',
    )
    expect(screen.getByText(/Nothing will stop this campaign automatically/)).toBeInTheDocument()
    expect(await putBodies(fetchMock)).toEqual([
      { auto_pause_enabled: false, bounce_pause_pct: 8, complaint_pause_pct: 1.5 },
    ])
  })

  test('a threshold edit round-trips, saving both values and the current toggle state', async () => {
    const fetchMock = stubCard({
      pages: [OK, { ...OK, guardrails: { ...OK.guardrails, bounce_pause_pct: 5, complaint_pause_pct: 0.5 } }],
    })
    renderWithProviders(<GuardrailsCard campaignId="c1" />)

    fireEvent.change(await screen.findByLabelText('Bounce threshold'), { target: { value: '5' } })
    fireEvent.change(screen.getByLabelText('Complaint threshold'), { target: { value: '0.5' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thresholds' }))

    await waitFor(() => expect(screen.getByLabelText('Bounce threshold')).toHaveValue(5))
    expect(screen.getByLabelText('Complaint threshold')).toHaveValue(0.5)
    expect(await putBodies(fetchMock)).toEqual([
      { auto_pause_enabled: true, bounce_pause_pct: 5, complaint_pause_pct: 0.5 },
    ])
    // The save button retires once the draft matches the server again.
    expect(screen.queryByRole('button', { name: 'Save thresholds' })).not.toBeInTheDocument()
  })

  test('a threshold outside 0.1–100 is rejected inline and no request is sent', async () => {
    const fetchMock = stubCard({ pages: [OK] })
    renderWithProviders(<GuardrailsCard campaignId="c1" />)

    fireEvent.change(await screen.findByLabelText('Bounce threshold'), { target: { value: '120' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thresholds' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Bounce threshold must be between 0.1% and 100% — got 120%.')
    expect(putCount(fetchMock)).toBe(0)

    // A zero is refused for the same reason, and still nothing is sent.
    fireEvent.change(screen.getByLabelText('Bounce threshold'), { target: { value: '0' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thresholds' }))
    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent('must be between 0.1% and 100% — got 0%'),
    )
    expect(putCount(fetchMock)).toBe(0)
  })

  test('toggling with an invalid draft is refused too, rather than persisting a stale threshold', async () => {
    const fetchMock = stubCard({ pages: [OK] })
    renderWithProviders(<GuardrailsCard campaignId="c1" />)

    fireEvent.change(await screen.findByLabelText('Complaint threshold'), { target: { value: '' } })
    fireEvent.click(screen.getByRole('switch', { name: 'Turn automatic pausing off' }))

    expect(await screen.findByRole('alert')).toHaveTextContent("Complaint threshold can't be empty")
    expect(putCount(fetchMock)).toBe(0)
    expect(screen.getByRole('switch', { name: 'Turn automatic pausing off' })).toHaveAttribute(
      'aria-checked',
      'true',
    )
  })

  test('a rejected save is surfaced and says the previous settings still apply', async () => {
    stubCard({ pages: [OK], putStatus: 500 })
    renderWithProviders(<GuardrailsCard campaignId="c1" />)

    fireEvent.change(await screen.findByLabelText('Bounce threshold'), { target: { value: '6' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save thresholds' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('rejected')
    expect(alert).toHaveTextContent('previous settings are still in force')
    // The edit is kept so it isn't silently lost.
    expect(screen.getByLabelText('Bounce threshold')).toHaveValue(6)
  })

  test('a failed load is an error, not a campaign that looks unguarded', async () => {
    stubCard({ pages: { status: 500 } })
    renderWithProviders(<GuardrailsCard campaignId="c1" />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent("Couldn't load deliverability (500)")
    expect(screen.queryByRole('switch')).not.toBeInTheDocument()
    expect(screen.queryByText('This campaign has never been paused automatically.')).not.toBeInTheDocument()
    // The section header stays, so the failure is attributable to this card.
    const card = screen.getByRole('region', { name: 'Deliverability guardrails' })
    expect(within(card).getByRole('alert')).toBe(alert)
  })
})
