import { httpStatus, retryAfterSeconds, serverDetail } from '@/lib/rtk-error'

/**
 * The one place CRM request failures turn into words. Everything narrows
 * through `@/lib/rtk-error` instead of reading `error.status` inline, so a
 * transport failure (no numeric status) never gets reported as an HTTP refusal.
 *
 * The CRM service answers 409/422 with prose the user can act on ("currency
 * must be a three-letter ISO code"), so those statuses surface the server's own
 * sentence rather than a generic one.
 */
export function crmErrorMessage(error: unknown, fallback: string): string {
  const status = httpStatus(error)
  if (status === undefined) {
    return 'Could not reach the server. Check your connection and try again.'
  }
  switch (status) {
    case 400:
      return serverDetail(error) ?? 'That request was rejected. Check the fields and try again.'
    case 401:
      return 'Your session expired. Refresh the page and try again.'
    // CRM endpoints are gated on the `crm:read` / `crm:write` scopes, so a 403
    // is nearly always a missing scope rather than a bug — say which.
    case 403:
      return serverDetail(error) ?? 'You do not have access to CRM records. Ask an admin for the crm:read and crm:write permissions.'
    case 404:
      return 'That CRM record no longer exists — it may have been deleted.'
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
  if (status >= 500) return 'The server had a problem loading CRM data. Try again in a moment.'
  return serverDetail(error) ?? fallback
}
