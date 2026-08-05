import { httpStatus, isFetchBaseQueryError, retryAfterSeconds } from '@/lib/rtk-error'

/**
 * The one place agent RTK errors turn into words. Everything here narrows
 * through `@/lib/rtk-error` rather than poking at `error.status` directly, so a
 * transport failure (no numeric status) and an HTTP refusal never get confused
 * for one another.
 */

/** The server's own explanation, when it sent one worth showing. */
function serverDetail(error: unknown): string | undefined {
  if (!isFetchBaseQueryError(error)) return undefined
  const { data } = error
  if (typeof data === 'string' && data.trim()) return data.trim()
  if (typeof data !== 'object' || data === null) return undefined
  const body = data as { message?: unknown; error?: unknown }
  for (const value of [body.message, body.error]) {
    // `error` is often a machine code like `email_not_verified`; a code with no
    // spaces reads as noise in a sentence, so only surface prose.
    if (typeof value === 'string' && value.trim() && value.includes(' ')) return value.trim()
  }
  return undefined
}

/** Maps any agent request failure to a sentence, falling back to the caller's phrasing. */
export function agentErrorMessage(error: unknown, fallback: string): string {
  const status = httpStatus(error)
  if (status === undefined) {
    return 'Could not reach the server. Check your connection and try again.'
  }
  switch (status) {
    case 401:
      return 'Your session expired. Refresh the page and try again.'
    case 403:
      return 'You do not have permission to do that.'
    case 404:
      return 'That is no longer available — it may have been deleted.'
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

/**
 * Approval decisions get their own copy: the endpoint distinguishes 400
 * (the edited inputs were rejected), 404 (gone) and 409 (already decided or
 * expired), and collapsing those into one sentence tells the reviewer nothing
 * about whether to retry, re-edit, or walk away.
 */
export function approvalDecisionMessage(error: unknown): string {
  switch (httpStatus(error)) {
    case 400:
      return serverDetail(error) ?? 'The edited inputs were rejected. Check the fields and try again.'
    case 404:
      return 'This action no longer exists. It may have been cleared after the run ended.'
    case 409:
      return 'This action was already decided — it expired or someone else approved or rejected it.'
    default:
      return agentErrorMessage(error, 'This decision could not be saved. Try again.')
  }
}
