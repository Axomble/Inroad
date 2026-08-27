import { getRouteApi } from '@tanstack/react-router'
import { PageBody } from '@/components/layout/page'
import { useGetCampaignQuery } from './api'
import { MetricsPanel } from './metrics-panel'
import { ResultsPanel } from './results-panel'

const routeApi = getRouteApi('/app/campaigns/$id/')

/**
 * "Is this campaign working?" — the campaign-wide rollup, then the per-step,
 * per-variant breakdown that decomposes it.
 *
 * Reads `useGetCampaignQuery` rather than taking metrics as props: the layout
 * above has already fetched and cached this exact query, so the hook resolves
 * from cache without a second request, and the section stays independently
 * mountable (and independently testable) instead of coupling to a parent's
 * render.
 */
export function CampaignOverviewSection() {
  const { id } = routeApi.useParams()
  const { data, isLoading, error } = useGetCampaignQuery({ id })

  return (
    <PageBody>
      {/* The layout already renders the fetch error beside the stats; repeating
          it here would show the same failure twice on one screen. */}
      {!isLoading && !error && (
        <MetricsPanel campaignId={id} metrics={data?.metrics} trackingEnabled={data?.tracking_enabled} />
      )}

      {/* The rollup answers "is this working", this answers "which step and
          which copy". Owns its own loading/error states. */}
      <ResultsPanel campaignId={id} />
    </PageBody>
  )
}

export default CampaignOverviewSection
