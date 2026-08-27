import { getRouteApi } from '@tanstack/react-router'
import { PageBody } from '@/components/layout/page'
import { CampaignEnrollmentsList } from './campaign-enrollments-list'

const routeApi = getRouteApi('/app/campaigns/$id/leads')

/**
 * Who the campaign is sending to: enrolled contacts and their classified
 * replies. The list owns its own loading/empty/error states.
 */
export function CampaignLeadsSection() {
  const { id } = routeApi.useParams()

  return (
    <PageBody>
      <CampaignEnrollmentsList campaignId={id} />
    </PageBody>
  )
}

export default CampaignLeadsSection
