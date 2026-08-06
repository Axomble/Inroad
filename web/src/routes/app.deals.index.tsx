import { createFileRoute } from '@tanstack/react-router'
import { DealsBoardPage } from '@/features/crm/deals-board-page'

export const Route = createFileRoute('/app/deals/')({ component: DealsBoardPage })
