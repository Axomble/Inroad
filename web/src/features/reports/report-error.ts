import { httpStatus, serverDetail } from '@/lib/rtk-error'

/**
 * Failure copy for the campaign report.
 *
 * Only the statuses where naming this screen helps get their own sentence:
 * a 403 here means the caller's principal lacks `campaigns:read`, which is
 * worth saying because it's the one failure a retry will never fix. Everything
 * else falls through to the neutral wording, rather than restating the whole
 * status table a third time.
 *
 * Narrowed through `@/lib/rtk-error` so a transport failure (no numeric status)
 * is never reported as an HTTP refusal.
 */
export function reportErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === undefined) {
    return 'Could not reach the server, so this report is unavailable — not empty. Check your connection and try again.'
  }
  if (status === 403) {
    return (
      serverDetail(error) ??
      'You do not have permission to read campaign performance. This report needs the campaigns:read scope.'
    )
  }
  if (status === 401) return 'Your session expired. Refresh the page and try again.'
  if (status >= 500) return "The server couldn't build this report. Your campaign data is unaffected — try again in a moment."
  return serverDetail(error) ?? "Couldn't load campaign performance. Try again in a moment."
}
