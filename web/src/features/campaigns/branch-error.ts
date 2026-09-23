// Human copy for a failed branch write (PUT/DELETE /steps/{id}/branch). Every
// failure narrows through `@/lib/rtk-error`. The loop a `cycle` names is read
// by branch-loop.ts.
import { errorCode, errorField, httpStatus, serverDetail } from '@/lib/rtk-error'
import type { BranchValidationError, StepBranch } from './api'
import { CONDITIONS, TRACKING_REQUIRED_HINT } from './branch-draft'

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
  branch_changed: 'This condition changed elsewhere — review it and try again.',
  cycle:
    'That would make a loop: someone could be sent back to a step they already received. The steps in the loop are highlighted — change one of their exits.',
}

function isBranchErrorCode(code: string | undefined): code is BranchErrorCode {
  return code !== undefined && Object.hasOwn(COPY, code)
}

export function branchErrorMessage(error: unknown): string {
  const code = errorCode(error)
  if (isBranchErrorCode(code)) return COPY[code]
  if (httpStatus(error) === 404) return 'This step no longer exists.'
  return serverDetail(error) ?? 'Couldn’t save the condition. Please try again.'
}

/**
 * A 409 `branch_changed`: the branch the write was based on is no longer the
 * stored one. `current` is what the server has now — `null` when the step has
 * no branch any more. `undefined` for any other failure, or a `current` that
 * doesn't have a branch's shape (it is then not safe to show as the truth).
 */
export function changedBranch(error: unknown): { current: StepBranch | null } | undefined {
  if (errorCode(error) !== 'branch_changed') return undefined
  const current = errorField(error, 'current')
  if (current === null) return { current: null }
  return isStepBranch(current) ? { current } : undefined
}

function isStepBranch(value: unknown): value is StepBranch {
  if (typeof value !== 'object' || value === null) return false
  const branch = value as Record<string, unknown>
  const nullableString = (field: string) => branch[field] === null || typeof branch[field] === 'string'
  return (
    typeof branch.step_id === 'string' &&
    typeof branch.condition === 'string' &&
    CONDITIONS.some((option) => option.value === branch.condition) &&
    (branch.within_days === null || typeof branch.within_days === 'number') &&
    nullableString('reply_label_key') &&
    nullableString('yes_step_id') &&
    nullableString('no_step_id') &&
    typeof branch.updated_at === 'string'
  )
}
