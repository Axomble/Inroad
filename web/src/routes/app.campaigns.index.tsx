import { createFileRoute } from '@tanstack/react-router'
import { CampaignsPage } from '@/features/campaigns/campaigns-page'
import { parseListSearch, type ListSearch } from '@/lib/list-search'

/** `?q=` / `?sort=` are the shared list filter — see `lib/list-search.ts`. */
export const Route = createFileRoute('/app/campaigns/')({
  validateSearch: (search: Record<string, unknown>): ListSearch => parseListSearch(search),
  component: CampaignsPage,
})
