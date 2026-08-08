import { createFileRoute } from '@tanstack/react-router'
import { LoginForm } from '../features/auth/login-form'

/**
 * `return_to` carries an internal path (SPA route or the API's /oauth2/authorize)
 * to resume after login — set when an auth-guarded route (e.g. /oauth/consent) or
 * the OAuth authorize endpoint bounces an unauthenticated user here. The login form
 * validates and navigates to it on success.
 *
 * `google_error` is the reason code the backend's Google callback redirects back
 * with when sign-in didn't complete. This is the landing route for every Google
 * failure, including one started from /register: the provider callback has a
 * single redirect target, and a failed sign-in belongs on the sign-in screen.
 */
export const Route = createFileRoute('/')({
  validateSearch: (search: Record<string, unknown>): { return_to?: string; google_error?: string } => ({
    return_to: typeof search.return_to === 'string' ? search.return_to : undefined,
    google_error: typeof search.google_error === 'string' ? search.google_error : undefined,
  }),
  component: LoginForm,
})
