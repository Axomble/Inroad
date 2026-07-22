import { fireEvent, screen, waitFor } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { MetricsPanel } from './metrics-panel'
// Importing the feature api registers the enhanced tracking mutation + tag
// wiring on the shared emptyApi endpoints registry — required for the hooks
// to resolve and for invalidatesTags to take effect.
import { useGetCampaignQuery } from './api'

describe('MetricsPanel', () => {
  test('formats rates as percentages and labels opens as indicative', () => {
    renderWithProviders(
      <MetricsPanel
        campaignId="c-1"
        trackingEnabled
        metrics={{
          sent: 100,
          opens_indicative: 40,
          open_rate: 0.4,
          clicks: 10,
          click_rate: 0.1,
          replies: 5,
          reply_rate: 0.05,
          bounces: 2,
          bounce_rate: 0.02,
          unsubscribes: 1,
          unsub_rate: 0.01,
        }}
      />,
    )

    expect(screen.getByText('Indicative')).toBeInTheDocument()
    expect(screen.getByText('Reliable')).toBeInTheDocument()
    expect(screen.getByText('40.0%')).toBeInTheDocument()
    expect(screen.getByText('10.0%')).toBeInTheDocument()
    expect(screen.getByText('5.0%')).toBeInTheDocument()
  })

  test('shows "no data yet" instead of a bogus rate when nothing has sent', () => {
    renderWithProviders(<MetricsPanel campaignId="c-1" metrics={{ sent: 0 }} />)

    expect(screen.getAllByText('No data yet').length).toBeGreaterThan(0)
    expect(screen.queryByText(/NaN/)).not.toBeInTheDocument()
  })

  test('toggling tracking invalidates the campaign detail tag and triggers a refetch', async () => {
    // Mirrors how CampaignDetail actually uses these two together: a
    // subscribed getCampaign query feeding MetricsPanel's props. This is what
    // lets us observe the invalidation for real — a config-based
    // invalidatesTags doesn't go through api.util.invalidateTags (that's a
    // separate, manually-dispatched action creator), so the only reliable
    // signal that invalidation actually happened is the subscribed query
    // refetching on its own.
    let trackingEnabled = true
    const rawFetch = vi.fn(async (input: RequestInfo | URL) => {
      const req = input as Request
      if (req.url.endsWith('/campaigns/c-1/tracking')) {
        trackingEnabled = false
        return new Response(JSON.stringify({ tracking_enabled: trackingEnabled }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      }
      if (req.url.endsWith('/campaigns/c-1')) {
        return new Response(JSON.stringify({ id: 'c-1', tracking_enabled: trackingEnabled, metrics: { sent: 10 } }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        })
      }
      return new Response(null, { status: 404 })
    })
    vi.stubGlobal('fetch', rawFetch)

    function Harness() {
      const { data } = useGetCampaignQuery({ id: 'c-1' })
      return <MetricsPanel campaignId="c-1" metrics={data?.metrics} trackingEnabled={data?.tracking_enabled} />
    }

    renderWithProviders(<Harness />)

    const getCalls = () => rawFetch.mock.calls.filter((c) => (c[0] as Request).url.endsWith('/campaigns/c-1')).length
    const putCalls = () =>
      rawFetch.mock.calls.filter((c) => (c[0] as Request).url.endsWith('/campaigns/c-1/tracking')).length

    // Initial mount fetches the campaign detail once.
    await waitFor(() => expect(getCalls()).toBe(1))

    fireEvent.click(screen.getByRole('switch', { name: /toggle open and click tracking/i }))

    // The PUT fires...
    await waitFor(() => expect(putCalls()).toBe(1))
    // ...and the invalidated tag causes the still-subscribed getCampaign
    // query to refetch on its own, without any manual refetch call.
    await waitFor(() => expect(getCalls()).toBe(2))
  })
})
