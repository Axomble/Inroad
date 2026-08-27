import { getRouteApi } from '@tanstack/react-router'
import { PageBody } from '@/components/layout/page'
import { SendersPanel } from './senders-panel'
import { GuardrailsCard } from './guardrails-card'

const routeApi = getRouteApi('/app/campaigns/$id/preferences')

/**
 * The configuration that shapes every future send without touching threads
 * already in flight: which mailboxes send, and what will stop the campaign.
 *
 * These two sit together because they answer adjacent questions — a campaign
 * that paused itself is explained by its guardrails, and the fix is usually its
 * senders. Both own their own loading/error states.
 */
export function CampaignPreferencesSection() {
  const { id } = routeApi.useParams()

  return (
    <PageBody>
      <SendersPanel campaignId={id} />
      <GuardrailsCard campaignId={id} />
    </PageBody>
  )
}

export default CampaignPreferencesSection
