import { createFileRoute } from '@tanstack/react-router'
import { CompaniesPage } from '@/features/crm/companies-page'

export const Route = createFileRoute('/app/companies/')({ component: CompaniesPage })
