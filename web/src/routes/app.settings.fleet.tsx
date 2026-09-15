import { createFileRoute } from '@tanstack/react-router'
import { FleetPage } from '@/features/fleet/fleet-page'

export const Route = createFileRoute('/app/settings/fleet')({
  component: FleetPage,
})
