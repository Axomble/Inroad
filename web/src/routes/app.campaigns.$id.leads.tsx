import { createFileRoute } from '@tanstack/react-router'
import { CampaignLeadsSection } from '@/features/campaigns/campaign-leads-section'

export const Route = createFileRoute('/app/campaigns/$id/leads')({
  component: CampaignLeadsSection,
})
