import { createFileRoute } from '@tanstack/react-router'
import { MailboxesPage } from '@/features/mailboxes/mailboxes-page'

/**
 * Search params carry the Gmail / Microsoft 365 OAuth callback result: the
 * public `/oauth/google/callback` and `/oauth/microsoft/callback` handlers
 * 302-redirect the browser back here with either `?connected=<email>` (success)
 * or `?oauth_error=<reason>` (failure), always tagged `&provider=gmail|m365` so
 * the banner can render provider-correct copy. OauthCallbackBanner reads them
 * and then strips them from the URL.
 */
export const Route = createFileRoute('/app/mailboxes')({
  validateSearch: (
    search: Record<string, unknown>,
  ): { connected?: string; oauth_error?: string; provider?: 'gmail' | 'm365' } => ({
    connected: typeof search.connected === 'string' ? search.connected : undefined,
    oauth_error: typeof search.oauth_error === 'string' ? search.oauth_error : undefined,
    provider: search.provider === 'gmail' || search.provider === 'm365' ? search.provider : undefined,
  }),
  component: MailboxesPage,
})
