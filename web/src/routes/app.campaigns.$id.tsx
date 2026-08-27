import { createFileRoute } from '@tanstack/react-router'
import { CampaignDetailLayout } from '@/features/campaigns/campaign-detail-layout'

export const Route = createFileRoute('/app/campaigns/$id')({
  component: CampaignDetailLayout,
})
