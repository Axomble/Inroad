import { describe, expect, test } from 'vitest'
import type { StepBranch } from '../api'
import {
  CONDITIONS,
  describeBranch,
  draftFromBranch,
  toBranchRequest,
  validateDraft,
  withExit,
  type ConditionDraft,
  type DraftContext,
} from '../branch-draft'

const context: DraftContext = {
  stepId: 'a',
  stepIds: new Set(['a', 'b', 'c']),
  trackingEnabled: true,
  htmlEverywhere: true,
  replyLabels: [
    { key: 'interested', label: 'Interested', stopsEnrollment: true },
    { key: 'question', label: 'Question', stopsEnrollment: false },
  ],
}

const draft = (fields: Partial<ConditionDraft>): ConditionDraft => ({ ...draftFromBranch(null), ...fields })
const fields = (d: ConditionDraft, c: DraftContext = context) => validateDraft(d, c).map((p) => p.field)

const saved: StepBranch = {
  step_id: 'a',
  condition: 'replied',
  within_days: 5,
  reply_label_key: 'question',
  yes_step_id: 'c',
  no_step_id: null,
  updated_at: '2026-09-23T00:00:00Z',
}

describe('draftFromBranch', () => {
  test('a new condition starts as "opened within 3 days", both paths ending', () => {
    expect(draftFromBranch(null)).toEqual({
      condition: 'opened',
      withinDays: '3',
      replyLabelKey: '',
      yesStepId: '',
      noStepId: '',
    })
  })
  test('an existing branch fills the form, nulls as empty', () => {
    expect(draftFromBranch(saved)).toEqual({
      condition: 'replied',
      withinDays: '5',
      replyLabelKey: 'question',
      yesStepId: 'c',
      noStepId: '',
    })
  })
})

describe('validateDraft mirrors the server', () => {
  test('a valid draft has no problems', () => {
    expect(fields(draft({ yesStepId: 'b', noStepId: 'c' }))).toEqual([])
  })

  test('within_days is a whole number from 1 to 90 (invalid_within_days)', () => {
    for (const bad of ['', '0', '91', '2.5', 'abc']) expect(fields(draft({ withinDays: bad }))).toEqual(['withinDays'])
    for (const ok of ['1', '90']) expect(fields(draft({ withinDays: ok }))).toEqual([])
  })

  test('"always" needs no window at all', () => {
    expect(fields(draft({ condition: 'always', withinDays: '' }))).toEqual([])
  })

  test('open / click conditions need tracking and an HTML body (tracking_required)', () => {
    for (const condition of ['opened', 'not_opened', 'clicked'] as const) {
      expect(fields(draft({ condition }), { ...context, trackingEnabled: false })).toEqual(['condition'])
      expect(fields(draft({ condition }), { ...context, htmlEverywhere: false })).toEqual(['condition'])
    }
    // Reply conditions don't read tracking at all.
    expect(fields(draft({ condition: 'replied' }), { ...context, trackingEnabled: false })).toEqual([])
    // Unknown (still loading) is left to the server rather than guessed.
    expect(fields(draft({}), { ...context, trackingEnabled: undefined, htmlEverywhere: undefined })).toEqual([])
  })

  test('the tracking problem says what to do, in words', () => {
    const [problem] = validateDraft(draft({}), { ...context, trackingEnabled: false })
    expect(problem?.message).toMatch(/turn it on from the Overview tab/)
  })

  test('a reply label that stops the sequence can never route (reply_label_stops_sequence)', () => {
    const [problem] = validateDraft(draft({ condition: 'replied', replyLabelKey: 'interested' }), context)
    expect(problem).toEqual({ field: 'replyLabel', message: expect.stringMatching(/“Interested” stop the sequence/) })
    expect(fields(draft({ condition: 'replied', replyLabelKey: 'question' }))).toEqual([])
    expect(fields(draft({ condition: 'not_replied', replyLabelKey: 'question' }))).toEqual([])
  })

  test('a label that no longer exists is flagged (invalid_reply_label)', () => {
    expect(fields(draft({ condition: 'replied', replyLabelKey: 'deleted' }))).toEqual(['replyLabel'])
  })

  test('an exit can’t point back at its own step (cycle) or at a step that’s gone (unknown_target)', () => {
    expect(fields(draft({ yesStepId: 'a' }))).toEqual(['yes'])
    expect(fields(draft({ noStepId: 'zzz' }))).toEqual(['no'])
  })

  test('"always" ignores a stale No exit instead of flagging it (no_exit_not_allowed is never sent)', () => {
    expect(fields(draft({ condition: 'always', noStepId: 'a' }))).toEqual([])
    expect(toBranchRequest(draft({ condition: 'always', noStepId: 'b' }), null).no_step_id).toBeNull()
  })
})

describe('toBranchRequest', () => {
  test('sends only what the condition uses', () => {
    expect(toBranchRequest(draft({ condition: 'opened', replyLabelKey: 'question', yesStepId: 'b' }), null)).toEqual({
      condition: 'opened',
      within_days: 3,
      // A label can't ride along on a non-reply condition (invalid_reply_label).
      reply_label_key: null,
      yes_step_id: 'b',
      no_step_id: null,
      // A new condition: create-only.
      expected_updated_at: null,
    })
    expect(toBranchRequest(draft({ condition: 'always', withinDays: '9', yesStepId: 'c' }), null)).toEqual({
      condition: 'always',
      within_days: null,
      reply_label_key: null,
      yes_step_id: 'c',
      no_step_id: null,
      expected_updated_at: null,
    })
    expect(toBranchRequest(draft({ condition: 'replied', replyLabelKey: 'question' }), null).reply_label_key).toBe(
      'question',
    )
  })

  test('the concurrency token is sent back exactly as the server gave it, microseconds and all', () => {
    const token = '2026-09-24T10:15:30.123456Z'
    expect(toBranchRequest(draft({}), token).expected_updated_at).toBe(token)
    expect(withExit({ ...saved, updated_at: token }, 'yes', 'b').expected_updated_at).toBe(token)
  })

  test('withExit changes one exit and keeps the rest', () => {
    expect(withExit(saved, 'no', 'b')).toEqual({
      expected_updated_at: saved.updated_at,
      condition: 'replied',
      within_days: 5,
      reply_label_key: 'question',
      yes_step_id: 'c',
      no_step_id: 'b',
    })
  })
})

describe('describeBranch', () => {
  test('reads as the condition in words', () => {
    expect(describeBranch({ condition: 'opened', within_days: 3, reply_label_key: null })).toBe('Opened within 3 days')
    expect(describeBranch({ condition: 'not_replied', within_days: 1, reply_label_key: null })).toBe('No reply within 1 day')
    expect(describeBranch(saved, 'Question')).toBe('Replied · Question within 5 days')
    expect(describeBranch(saved)).toBe('Replied · question within 5 days')
    expect(describeBranch({ condition: 'always', within_days: null, reply_label_key: null })).toBe('Always')
  })

  test('every contract condition has an option', () => {
    expect(CONDITIONS.map((c) => c.value).sort()).toEqual(
      ['always', 'clicked', 'not_opened', 'not_replied', 'opened', 'replied'].sort(),
    )
  })
})
