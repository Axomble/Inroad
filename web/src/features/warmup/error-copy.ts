import { httpStatus, serverDetail } from '@/lib/rtk-error'

/**
 * Warmup's own copy for a failed *read*.
 *
 * The failure mode this wording guards against is specific to warmup: an empty
 * history is the normal state for a new participant, so a failed request that
 * renders as "nothing has happened" tells an operator their mailbox has never
 * changed state when in fact nothing is known. Every branch below reads as a
 * request that failed.
 *
 * Statuses are narrowed through `@/lib/rtk-error`, so a transport failure — which
 * carries a string status tag, not a number — is never reported as an HTTP
 * refusal.
 */
export function warmupErrorMessage(error: unknown, fallback: string): string {
  const status = httpStatus(error)
  if (status === undefined) {
    return 'Could not reach the server, so nothing about this mailbox is known right now. Check your connection and try again.'
  }
  switch (status) {
    case 401:
      return 'Your session expired. Refresh the page and try again.'
    case 403:
      return "You don't have access to this workspace's warmup data."
    case 404:
      return 'This mailbox is no longer a warmup participant in this workspace — refresh the page.'
    default:
      break
  }
  if (status >= 500) return 'The server had a problem answering, so this could not be loaded. Try again in a moment.'
  return serverDetail(error) ?? fallback
}
