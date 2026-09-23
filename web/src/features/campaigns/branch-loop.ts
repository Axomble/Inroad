// The loop a 422 `cycle` names — from a branch write, a step delete or a
// reorder, which all answer the same shape. Its own module, apart from the
// condition copy in branch-error.ts, because the eagerly loaded sequence editor
// needs this (a delete can close a loop) and nothing else of branching: keeping
// it separate keeps the condition editor's copy and rules in the lazy canvas
// chunk.
import { errorCode, errorField } from '@/lib/rtk-error'

/**
 * The steps on the loop, in path order; `null` for any other failure, or a
 * cycle that didn't list its steps in a shape that can be trusted.
 */
export function cycleStepIds(error: unknown): string[] | null {
  if (errorCode(error) !== 'cycle') return null
  const ids = errorField(error, 'step_ids')
  if (!Array.isArray(ids) || !ids.every((id): id is string => typeof id === 'string')) return null
  return ids.length > 0 ? ids : null
}
