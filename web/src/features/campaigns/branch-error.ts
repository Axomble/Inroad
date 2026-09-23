// Human copy for a failed branch write (PUT/DELETE /steps/{id}/branch). Every
// failure narrows through `@/lib/rtk-error`. The loop a `cycle` names is read
// by branch-loop.ts.
import { errorCode, httpStatus, serverDetail } from '@/lib/rtk-error'
import type { BranchValidationError } from './api'
import { TRACKING_REQUIRED_HINT } from './branch-draft'

type BranchErrorCode = BranchValidationError['code']

const COPY: Record<BranchErrorCode, string> = {
  invalid_condition: 'That condition isn’t one Inroad supports. Pick one from the list.',
  invalid_within_days: 'The window must be a whole number of days from 1 to 90.',
  invalid_reply_label: 'That reply label no longer exists. Pick another, or route on any reply.',
  reply_label_stops_sequence:
    'Replies with that label stop the sequence before any condition runs, so it could never route anyone. Pick a label that doesn’t stop the sequence.',
  tracking_required: `${TRACKING_REQUIRED_HINT} Turn tracking on from this campaign’s Overview tab, or route on replies instead.`,
  no_exit_not_allowed: '“Always” has only one exit — there is no “No” path to set.',
  unknown_step: 'This step no longer exists.',
  unknown_target: 'An exit points at a step that no longer exists. Pick again.',
  cycle:
    'That would make a loop: someone could be sent back to a step they already received. The steps in the loop are highlighted — change one of their exits.',
}

function isBranchErrorCode(code: string | undefined): code is BranchErrorCode {
  return code !== undefined && code in COPY
}

export function branchErrorMessage(error: unknown): string {
  const code = errorCode(error)
  if (isBranchErrorCode(code)) return COPY[code]
  if (httpStatus(error) === 404) return 'This step no longer exists.'
  return serverDetail(error) ?? 'Couldn’t save the condition. Please try again.'
}
