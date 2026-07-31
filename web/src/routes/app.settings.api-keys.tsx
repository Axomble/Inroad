import { createFileRoute } from '@tanstack/react-router'
import { ApiKeysPanel } from '@/features/auth/api-keys-panel'

export const Route = createFileRoute('/app/settings/api-keys')({
  component: ApiKeysPanel,
})
