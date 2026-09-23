import { describe, expect, test } from 'vitest'
import { branchErrorMessage } from '../branch-error'
import { cycleStepIds } from '../branch-loop'
import { reorderErrorMessage, stepErrorMessage } from '../step-error'

const refused = (status: number, code: string, extra: Record<string, unknown> = {}) => ({
  status,
  data: { error: 'server words', code, ...extra },
})

describe('branchErrorMessage', () => {
  test.each([
    [400, 'invalid_condition', /isn’t one Inroad supports/],
    [400, 'invalid_within_days', /whole number of days from 1 to 90/],
    [400, 'invalid_reply_label', /reply label no longer exists/],
    [400, 'reply_label_stops_sequence', /stop the sequence before any condition runs/],
    [400, 'tracking_required', /tracking is on for this campaign.*Overview tab/],
    [400, 'no_exit_not_allowed', /only one exit/],
    [400, 'cycle', /would make a loop/],
    [422, 'cycle', /steps in the loop are highlighted/],
    [422, 'unknown_target', /step that no longer exists/],
    [422, 'unknown_step', /This step no longer exists/],
  ])('%i %s has its own copy', (status, code, copy) => {
    expect(branchErrorMessage(refused(status, code))).toMatch(copy)
  })

  test('a 404 without a code, then the server’s prose, then a generic line', () => {
    expect(branchErrorMessage({ status: 404, data: { error: 'step not found' } })).toBe('This step no longer exists.')
    expect(branchErrorMessage({ status: 500, data: { error: 'the database is on fire' } })).toBe('the database is on fire')
    expect(branchErrorMessage({ status: 'FETCH_ERROR', error: 'offline' })).toMatch(/Couldn’t save the condition/)
  })
})

describe('cycleStepIds', () => {
  test('reads the loop a cycle names, in order', () => {
    expect(cycleStepIds(refused(422, 'cycle', { step_ids: ['b', 'c', 'a'] }))).toEqual(['b', 'c', 'a'])
  })
  test('null for any other failure, or a loop it can’t trust', () => {
    expect(cycleStepIds(refused(422, 'unknown_target', { step_ids: ['b'] }))).toBeNull()
    expect(cycleStepIds(refused(422, 'cycle'))).toBeNull()
    expect(cycleStepIds(refused(422, 'cycle', { step_ids: [] }))).toBeNull()
    expect(cycleStepIds(refused(422, 'cycle', { step_ids: [1, 2] }))).toBeNull()
    expect(cycleStepIds(undefined)).toBeNull()
  })
})

test('a structural write that would close a loop says so, not "couldn’t reorder"', () => {
  expect(reorderErrorMessage(refused(422, 'cycle'))).toMatch(/loop through a condition/)
  expect(stepErrorMessage(refused(422, 'cycle'))).toMatch(/loop through a condition/)
  // Unchanged for everything else.
  expect(reorderErrorMessage({ status: 409, data: {} })).toMatch(/only allowed while the campaign is a draft/)
})
