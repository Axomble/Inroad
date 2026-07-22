import { createFileRoute } from '@tanstack/react-router'
import { InvitesPanel } from '@/features/team/invites-panel'

export const Route = createFileRoute('/app/settings/team')({
  component: InvitesPanel,
})
