/**
 * Map a mailbox's backend status to a StatusPill tone + human label. Keeps the
 * status vocabulary in one place as more states (warming, etc.) arrive.
 */
export type MailboxTone = 'running' | 'paused' | 'failing' | 'draft'

export function mailboxTone(status?: string): MailboxTone {
  switch (status) {
    case 'active':
      return 'running'
    case 'paused':
      return 'paused'
    case 'error':
      return 'failing'
    default:
      return 'draft'
  }
}

export function mailboxStatusLabel(status?: string): string {
  switch (status) {
    case 'active':
      return 'Active'
    case 'paused':
      return 'Paused'
    case 'error':
      return 'Error'
    default:
      return status ?? 'Unknown'
  }
}
