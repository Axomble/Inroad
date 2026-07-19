import { createFileRoute } from '@tanstack/react-router'
import { MailboxesPage } from '@/features/mailboxes/mailboxes-page'

export const Route = createFileRoute('/app/mailboxes')({
  component: MailboxesPage,
})
