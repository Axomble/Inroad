import { createFileRoute } from '@tanstack/react-router'
import { CRMPage } from '@/features/crm/crm-page'

export const Route = createFileRoute('/app/crm')({ component: CRMPage })
