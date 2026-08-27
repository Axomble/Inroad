import { getRouteApi } from '@tanstack/react-router'
import { PageBody } from '@/components/layout/page'
import { SchedulePanel } from './schedule-panel'
import { SendersPanel } from './senders-panel'

const routeApi = getRouteApi('/app/campaigns/$id/schedule')

/**
 * Everything that decides a campaign's outbound throughput: when it may send
 * (the weekly window board and its timezone), how much it may send, and which
 * mailboxes it sends through.
 *
 * The pool sits here rather than with the other preferences because the daily
 * limit is defined ACROSS it — "the most this campaign sends per day in total,
 * added up across every sender in its pool". An operator setting that number
 * has to see how many mailboxes it divides between; on a separate tab the
 * figure is unreadable, and under-configuring it by the size of the pool is the
 * mistake the limit's own help text exists to prevent.
 */
export function CampaignScheduleSection() {
  const { id } = routeApi.useParams()

  return (
    <PageBody>
      <SchedulePanel campaignId={id} />
      <SendersPanel campaignId={id} />
    </PageBody>
  )
}

export default CampaignScheduleSection
