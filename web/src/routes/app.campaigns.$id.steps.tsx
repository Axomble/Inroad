import { createFileRoute } from '@tanstack/react-router'
import { CampaignStepsSection } from '@/features/campaigns/campaign-steps-section'

export const Route = createFileRoute('/app/campaigns/$id/steps')({
  component: CampaignStepsSection,
})
