import { createFileRoute } from '@tanstack/react-router'
import { ApprovalsPage } from '@/features/agent/approvals-page'

export const Route = createFileRoute('/app/approvals')({
  component: ApprovalsPage,
})
