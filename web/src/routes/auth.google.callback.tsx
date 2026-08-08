import { createFileRoute } from '@tanstack/react-router'
import { GoogleCallbackPage } from '@/features/auth/google-callback-page'

/**
 * Where a successful Google sign-in lands: `?signin=ok`, plus `?return_to=<path>`
 * when one was requested at the start of the flow. Public — arriving here is how a
 * session comes into existence, so it cannot sit behind the auth guard. Failures
 * do not come here; the backend sends those to `/?google_error=<reason>`.
 */
export const Route = createFileRoute('/auth/google/callback')({
  validateSearch: (search: Record<string, unknown>): { signin?: string; return_to?: string } => ({
    signin: typeof search.signin === 'string' ? search.signin : undefined,
    return_to: typeof search.return_to === 'string' ? search.return_to : undefined,
  }),
  component: GoogleCallbackPage,
})
