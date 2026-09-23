// The words the audit log screen is made of.
//
// Kept out of JSX for the reason the fleet and dead-letter copy modules give: on
// an evidence screen the wording is the feature. The one distinction that
// matters most here is WHO: an audit row that names the wrong actor — or names a
// person where there was only an anonymous attempt — is worse than no row.
//
// Every string built here may carry attacker-controlled text (an email someone
// typed into the sign-in form, a user agent, a metadata value). They are only
// ever returned as plain strings for React to render as text; nothing here
// produces markup.
import { httpStatus, serverDetail } from '@/lib/rtk-error'
import type { AuditEvent } from '@/store/api'

export const PAGE_INTRO =
  'Who did what in this workspace, newest first. Every sign-in, membership change, credential and campaign state change is recorded here as it happens, and nothing in this log can be edited or removed.'

export const ADMINS_ONLY_TITLE = 'Owners and admins only'

export const ADMINS_ONLY_DESCRIPTION =
  'The audit log names who signed in, from where, and what they changed. Ask a workspace owner or admin if you need something from it.'

export const EMPTY_TITLE = 'Nothing has been recorded yet'

export const EMPTY_DESCRIPTION = 'Events appear here the moment someone signs in or changes something in this workspace.'

export const EMPTY_FILTERED_TITLE = 'No events match these filters'

export const EMPTY_FILTERED_DESCRIPTION = 'Widen the date range or clear a filter to see more of the log.'

export const INVERTED_RANGE = 'The end date is before the start date. Pick an end date on or after the start.'

export const END_OF_LOG = 'That is everything for these filters.'

export interface ActorCopy {
  primary: string
  /** Whose authority the actor used, or which component — omitted when there is nothing to add. */
  secondary?: string
}

/**
 * Who performed an event, as a reader would say it.
 *
 * A user actor with no id is not a user at all: it is an attempt nobody
 * authenticated, i.e. a failed sign-in. Rendering it as "Deleted user" or as the
 * email that was typed would attribute the attempt to an account that may never
 * have been involved, so it gets its own label and the typed email is shown as
 * what it is — something someone tried.
 */
export function actorCopy(event: AuditEvent): ActorCopy {
  const email = event.actor_email ?? undefined
  switch (event.actor_type) {
    case 'user': {
      if (event.actor_id === null) {
        const tried = event.metadata.email
        return { primary: 'Unknown (failed sign-in)', secondary: tried ? `tried ${tried}` : undefined }
      }
      return { primary: email ?? 'Deleted user' }
    }
    case 'api_key':
      return { primary: `API key ${event.actor_id ?? 'unknown'}`, secondary: email && `created by ${email}` }
    case 'oauth_client':
      return { primary: `Connected app ${event.actor_id ?? 'unknown'}`, secondary: email && `for ${email}` }
    case 'agent':
      return { primary: 'Agent', secondary: email && `for ${email}` }
    case 'system':
      return { primary: 'System', secondary: event.actor_id ?? undefined }
    default:
      // A newer server's actor type: show what it is called rather than guess.
      return { primary: String(event.actor_type), secondary: email }
  }
}

/** "campaign · 5f1c…" — or null when the event had no target (a sign-in). */
export function targetCopy(event: AuditEvent): string | null {
  if (event.target_type === null && event.target_id === null) return null
  return [event.target_type, event.target_id].filter((part) => part !== null).join(' · ')
}

/** Metadata as ordered pairs; sorted so the same event reads the same way every time. */
export function metadataEntries(metadata: AuditEvent['metadata']): [string, string][] {
  return Object.entries(metadata).sort(([a], [b]) => a.localeCompare(b))
}

export function auditLogErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === undefined) {
    return "Couldn't reach the server, so the audit log can't be shown right now. Check your connection and try again."
  }
  switch (status) {
    case 401:
      return 'Your session expired. Refresh the page and sign in again to view the audit log.'
    case 403:
      return 'The audit log is visible to workspace owners and admins only, and this account is neither. If your role changed recently, refresh the page.'
    case 400: {
      const detail = serverDetail(error)
      return detail
        ? `The server refused these filters (${detail}). Clear them and try again.`
        : 'The server refused these filters. Clear them and try again.'
    }
    default:
      return serverDetail(error) ?? "Couldn't load the audit log. Try again."
  }
}
