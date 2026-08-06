import { createFileRoute } from '@tanstack/react-router'
import { ThreadDetailPage } from '@/features/inbox/thread-detail-page'

export const Route = createFileRoute('/app/inbox/$threadId')({
  component: ThreadDetailPage,
})
