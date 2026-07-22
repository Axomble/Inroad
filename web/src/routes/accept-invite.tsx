import { createFileRoute } from '@tanstack/react-router'
import { AcceptInvitePage } from '@/features/auth/accept-invite-page'

export const Route = createFileRoute('/accept-invite')({
  validateSearch: (search: Record<string, unknown>): { token?: string } => ({
    token: typeof search.token === 'string' ? search.token : undefined,
  }),
  component: AcceptInvitePage,
})
