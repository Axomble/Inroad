import { httpStatus, serverDetail } from '@/lib/rtk-error'
import { recordErrorMessage } from '@/features/records/error-copy'

/**
 * CRM request failures in words.
 *
 * Only the three statuses where naming CRM beats staying neutral live here — a
 * missing scope, a deleted record, and a server fault — because those sentences
 * tell the reader which permission to ask for and which data failed. Everything
 * else defers to `recordErrorMessage`, so there is one status-to-sentence mapping
 * rather than two that drift.
 *
 * The CRM service answers 409/422 with prose the user can act on ("currency must
 * be a three-letter ISO code"); that surfacing happens in the shared mapping.
 */
export function crmErrorMessage(error: unknown, fallback: string): string {
  const status = httpStatus(error)
  switch (status) {
    // CRM endpoints are gated on the `crm:read` / `crm:write` scopes, so a 403
    // is nearly always a missing scope rather than a bug — say which.
    case 403:
      return serverDetail(error) ?? 'You do not have access to CRM records. Ask an admin for the crm:read and crm:write permissions.'
    case 404:
      return 'That CRM record no longer exists — it may have been deleted.'
    default:
      break
  }
  if (status !== undefined && status >= 500) {
    return 'The server had a problem loading CRM data. Try again in a moment.'
  }
  return recordErrorMessage(error, fallback)
}
