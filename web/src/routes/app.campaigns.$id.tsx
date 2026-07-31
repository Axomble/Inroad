import { createFileRoute } from '@tanstack/react-router'
import { CampaignDetailPage } from '@/features/campaigns/campaign-detail-page'

export const Route = createFileRoute('/app/campaigns/$id')({
  component: CampaignDetailPage,
})
