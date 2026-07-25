/**
 * Persisted mailbox `provider` values — as stored by the API, returned on the
 * Mailbox DTO, and echoed on the OAuth callback redirect (`&provider=`). This
 * is distinct from the connect-flow `OauthProvider` key ('microsoft') used only
 * to pick a start endpoint; the persisted value for that provider is 'm365'.
 */
export type MailboxProvider = 'gmail' | 'm365' | 'smtp'

/**
 * Human label for a persisted mailbox provider. gmail → "Gmail", m365 →
 * "Microsoft 365"; anything else (SMTP/IMAP or an unknown/absent value) returns
 * undefined so each caller supplies its own fallback — "Mailbox" for the
 * connect banner, "SMTP" for the row chip. One source of truth for the labels.
 */
export function mailboxProviderLabel(provider: string | undefined): string | undefined {
  switch (provider) {
    case 'gmail':
      return 'Gmail'
    case 'm365':
      return 'Microsoft 365'
    default:
      return undefined
  }
}
