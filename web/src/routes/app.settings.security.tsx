import { createFileRoute } from '@tanstack/react-router'
import { SecurityPage } from '@/features/auth/security-page'

export const Route = createFileRoute('/app/settings/security')({
  component: SecurityPage,
})
