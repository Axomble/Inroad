import { httpStatus, retryAfterSeconds, serverDetail } from '@/lib/rtk-error'

/**
 * The one place a record request failure turns into words, for any record type.
 * Everything narrows through `@/lib/rtk-error` instead of reading `error.status`
 * inline, so a transport failure (no numeric status) never gets reported as an
 * HTTP refusal.
 *
 * Deliberately names no domain and no scope: notes, tasks and activity attach to
 * contacts, companies and deals alike, and the scope a 403 is about differs by
 * record type. A domain with something more specific to say layers over this —
 * see `features/crm/error-copy.ts`, which owns the three statuses where naming
 * CRM and its scopes is more useful than staying neutral.
 */
export function recordErrorMessage(error: unknown, fallback: string): string {
  const status = httpStatus(error)
  if (status === undefined) {
    return 'Could not reach the server. Check your connection and try again.'
  }
  switch (status) {
    case 400:
      return serverDetail(error) ?? 'That request was rejected. Check the fields and try again.'
    case 401:
      return 'Your session expired. Refresh the page and try again.'
    case 403:
      return serverDetail(error) ?? 'You do not have permission to do that.'
    case 404:
      return 'That record no longer exists — it may have been deleted.'
    case 409:
      return serverDetail(error) ?? 'That conflicts with an existing record. Reload and try again.'
    case 422:
      return serverDetail(error) ?? 'Some values were rejected. Check the fields and try again.'
    case 429: {
      const seconds = retryAfterSeconds(error)
      return seconds
        ? `Too many requests. Try again in ${seconds} second${seconds === 1 ? '' : 's'}.`
        : 'Too many requests. Try again in a moment.'
    }
    default:
      break
  }
  if (status >= 500) return 'The server had a problem. Try again in a moment.'
  return serverDetail(error) ?? fallback
}
