import { createFileRoute } from '@tanstack/react-router'
import { CompanyDetailPage } from '@/features/crm/company-detail-page'

export const Route = createFileRoute('/app/companies/$id')({ component: CompanyRoute })

function CompanyRoute() {
  const { id } = Route.useParams()
  return <CompanyDetailPage companyId={id} />
}
