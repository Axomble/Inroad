import { getRouteApi } from '@tanstack/react-router'
import { PageBody } from '@/components/layout/page'
import { SchedulePanel } from './schedule-panel'

const routeApi = getRouteApi('/app/campaigns/$id/schedule')

/**
 * When the campaign sends: the weekly send-window board and its timezone.
 *
 * Its own section rather than a card among others, because the board is a
 * direct-manipulation surface — drawing and dragging blocks needs the width,
 * and it is the heaviest chunk on this route.
 */
export function CampaignScheduleSection() {
  const { id } = routeApi.useParams()

  return (
    <PageBody>
      <SchedulePanel campaignId={id} />
    </PageBody>
  )
}

export default CampaignScheduleSection
