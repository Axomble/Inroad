import { createFileRoute } from '@tanstack/react-router'
import { MailboxesPage } from '@/features/mailboxes/mailboxes-page'

/**
 * Search params carry the Gmail OAuth callback result: the public
 * `/oauth/google/callback` handler 302-redirects the browser back here with
 * either `?connected=<email>` (success) or `?oauth_error=<reason>` (failure).
 * OauthCallbackBanner reads them and then strips them from the URL.
 */
export const Route = createFileRoute('/app/mailboxes')({
  validateSearch: (search: Record<string, unknown>): { connected?: string; oauth_error?: string } => ({
    connected: typeof search.connected === 'string' ? search.connected : undefined,
    oauth_error: typeof search.oauth_error === 'string' ? search.oauth_error : undefined,
  }),
  component: MailboxesPage,
})
