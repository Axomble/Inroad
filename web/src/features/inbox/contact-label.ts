import type { InboxThreadSummary } from './api'

/**
 * The thread's primary identity — who replied, not which mailbox received it
 * or what the subject line says: knowing WHO is the core value of an inbox.
 * `InboxThreadSummary` carries the contact's name/email straight on the
 * thread (no separate lookup needed); a legacy direct-send match (no linked
 * contact at all) has none of the three, and falls back to a neutral label
 * rather than rendering blank.
 *
 * Lives in its own module (not exported from `thread-list.tsx`) so the list
 * row and the thread detail header share one definition without a component
 * file exporting a non-component value (which breaks fast refresh).
 */
export function contactLabel(thread: InboxThreadSummary): string {
  const name = `${thread.contact_first_name} ${thread.contact_last_name}`.trim()
  if (name) return name
  if (thread.contact_email) return thread.contact_email
  return 'Unknown sender'
}
