import { createFileRoute } from '@tanstack/react-router'
import { ResetPasswordPage } from '@/features/auth/reset-password-page'

export const Route = createFileRoute('/reset-password')({
  validateSearch: (search: Record<string, unknown>): { token?: string } => ({
    token: typeof search.token === 'string' ? search.token : undefined,
  }),
  component: ResetPasswordPage,
})
