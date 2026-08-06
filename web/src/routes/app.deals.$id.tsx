import { createFileRoute } from '@tanstack/react-router'
import { DealDetailPage } from '@/features/crm/deal-detail-page'

export const Route = createFileRoute('/app/deals/$id')({ component: DealRoute })

function DealRoute() {
  const { id } = Route.useParams()
  return <DealDetailPage dealId={id} />
}
