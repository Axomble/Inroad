import { createFileRoute } from '@tanstack/react-router'
import { CampaignOverviewSection } from '@/features/campaigns/campaign-overview-section'

// `autoCodeSplitting` in vite.config.ts splits every route into its own chunk,
// so each campaign tab loads on first visit rather than with the campaign
// header — no hand-written `lazy()` needed here.
export const Route = createFileRoute('/app/campaigns/$id/')({
  component: CampaignOverviewSection,
})
