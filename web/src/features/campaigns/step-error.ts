// Shared typed-error message mapping for sequence-step mutations. Lives in its
// own (component-free) module so both the add/edit form and the delete dialog
// surface the SAME human copy for the same HTTP status, narrowed via the typed
// `httpStatus` helper instead of ad-hoc `'status' in err` checks.
import { httpStatus } from '@/lib/rtk-error'

/** Maps an RTK Query error from a step mutation to a human message. */
export function stepErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === 409) return 'Structural changes are only allowed while the campaign is a draft.'
  if (status === 404) return 'That step no longer exists.'
  if (status === 400) return 'Please fill in all required fields.'
  return "Couldn't save the step. Please try again."
}
