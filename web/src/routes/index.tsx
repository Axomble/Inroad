import { createFileRoute } from '@tanstack/react-router'
import { LoginForm } from '../features/auth/login-form'

/**
 * `return_to` carries an internal path (SPA route or the API's /oauth2/authorize)
 * to resume after login — set when an auth-guarded route (e.g. /oauth/consent) or
 * the OAuth authorize endpoint bounces an unauthenticated user here. The login form
 * validates and navigates to it on success.
 */
export const Route = createFileRoute('/')({
  validateSearch: (search: Record<string, unknown>): { return_to?: string } => ({
    return_to: typeof search.return_to === 'string' ? search.return_to : undefined,
  }),
  component: LoginForm,
})
