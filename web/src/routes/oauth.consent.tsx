import { createFileRoute, redirect } from '@tanstack/react-router'
import { runAuthBootstrap } from '@/features/auth/use-auth-bootstrap'
import { OAuthConsentPage } from '@/features/auth/oauth-consent-page'

/**
 * OAuth 2.1 consent screen (`/oauth/consent?consent_id=…`). The backend's
 * /oauth2/authorize sends a logged-in resource owner here to approve/deny a
 * third-party app.
 *
 * Auth-guarded like the /app routes: on a fresh load the in-memory session hasn't
 * been restored yet (`status === 'idle'`), so we await the silent-refresh bootstrap
 * before deciding. If there is still no session, we bounce to the login screen with a
 * `return_to` back to this exact URL so the user resumes here after signing in
 * (mirrors the login return_to the backend uses when /authorize is hit
 * unauthenticated). `return_to` is consumed by the `/` route + login form.
 */
export const Route = createFileRoute('/oauth/consent')({
  validateSearch: (search: Record<string, unknown>): { consent_id?: string } => ({
    consent_id: typeof search.consent_id === 'string' ? search.consent_id : undefined,
  }),
  beforeLoad: async ({ context, location }) => {
    const { store } = context
    if (store.getState().auth.status === 'idle') {
      await runAuthBootstrap(store.dispatch)
    }
    if (!store.getState().auth.accessToken) {
      throw redirect({ to: '/', search: { return_to: location.href } })
    }
  },
  component: OAuthConsentPage,
})
