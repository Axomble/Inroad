import { getRouteApi } from '@tanstack/react-router'
import { PageBody } from '@/components/layout/page'
import { useGetCampaignQuery } from './api'
import { SequenceEditor } from './sequence-editor'

const routeApi = getRouteApi('/app/campaigns/$id/steps')

/**
 * What the campaign says: the step sequence and its variants.
 *
 * `status` gates editing inside the editor (a running campaign restricts which
 * steps can change), and comes from the cached campaign query the layout
 * already issued.
 */
export function CampaignStepsSection() {
  const { id } = routeApi.useParams()
  const { data } = useGetCampaignQuery({ id })

  return (
    <PageBody>
      <SequenceEditor campaignId={id} status={data?.status} />
    </PageBody>
  )
}

export default CampaignStepsSection
