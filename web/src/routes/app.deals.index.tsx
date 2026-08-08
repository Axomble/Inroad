import { createFileRoute } from '@tanstack/react-router'
import { DealsPage } from '@/features/crm/deals-page'

export const Route = createFileRoute('/app/deals/')({ component: DealsPage })
