import { createFileRoute } from '@tanstack/react-router'
import { OutboxPage } from '@/features/inbox/outbox-page'

export const Route = createFileRoute('/app/outbox')({
  component: OutboxPage,
})
