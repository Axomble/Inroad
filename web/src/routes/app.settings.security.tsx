import { createFileRoute } from '@tanstack/react-router'
import { ActiveSessions } from '@/features/auth/active-sessions'

export const Route = createFileRoute('/app/settings/security')({
  component: ActiveSessions,
})
