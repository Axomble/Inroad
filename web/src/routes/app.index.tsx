import { createFileRoute, redirect } from '@tanstack/react-router'

// /app -> land on Mailboxes (the first step of the outbound workflow).
export const Route = createFileRoute('/app/')({
  beforeLoad: () => {
    throw redirect({ to: '/app/mailboxes' })
  },
})
