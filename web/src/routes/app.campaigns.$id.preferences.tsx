import { createFileRoute } from '@tanstack/react-router'
import { CampaignPreferencesSection } from '@/features/campaigns/campaign-preferences-section'

export const Route = createFileRoute('/app/campaigns/$id/preferences')({
  component: CampaignPreferencesSection,
})
