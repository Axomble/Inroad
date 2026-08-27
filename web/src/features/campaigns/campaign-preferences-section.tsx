import { getRouteApi } from '@tanstack/react-router'
import { PageBody } from '@/components/layout/page'
import { GuardrailsCard } from './guardrails-card'

const routeApi = getRouteApi('/app/campaigns/$id/preferences')

/**
 * What will stop the campaign: the auto-pause thresholds, and the pause events
 * that have already fired.
 *
 * A campaign that paused itself is answered here and nowhere else. Owns its own
 * loading and error states.
 */
export function CampaignPreferencesSection() {
  const { id } = routeApi.useParams()

  return (
    <PageBody>
      <GuardrailsCard campaignId={id} />
    </PageBody>
  )
}

export default CampaignPreferencesSection
