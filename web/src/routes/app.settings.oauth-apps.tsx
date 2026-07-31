import { createFileRoute } from '@tanstack/react-router'
import { ConnectedAppsPanel } from '@/features/auth/connected-apps-panel'

export const Route = createFileRoute('/app/settings/oauth-apps')({
  component: ConnectedAppsPanel,
})
