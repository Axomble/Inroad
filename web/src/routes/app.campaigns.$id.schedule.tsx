import { createFileRoute } from '@tanstack/react-router'
import { CampaignScheduleSection } from '@/features/campaigns/campaign-schedule-section'

export const Route = createFileRoute('/app/campaigns/$id/schedule')({
  component: CampaignScheduleSection,
})
