// The condition editor's model, and the checks it runs before a save. Pure: no
// React, no RTK. The rules mirror the server's (internal/platform/seqgraph
// `ValidateShape`, internal/app/sequencestep `checkReplyLabel` /
// `checkTrackable`), so an editor that says "fine" is one the API will accept —
// and where the client can't know (a label deleted a moment ago), the server's
// typed error still has the last word (see branch-error.ts).
import type { StepBranch, StepBranchCondition, StepBranchRequest } from './api'

export const MIN_WITHIN_DAYS = 1
export const MAX_WITHIN_DAYS = 90
const DEFAULT_WITHIN_DAYS = 3

type ConditionMeta = {
  value: StepBranchCondition
  /** The select's option text. */
  label: string
  /** Reads opens or clicks, which only exist with tracking and an HTML body. */
  needsTracking: boolean
  /** Reads replies, so it may be narrowed to one reply label. */
  isReply: boolean
}

export const CONDITIONS: readonly ConditionMeta[] = [
  { value: 'opened', label: 'Opened this email', needsTracking: true, isReply: false },
  { value: 'not_opened', label: 'Did not open this email', needsTracking: true, isReply: false },
  { value: 'clicked', label: 'Clicked a link in this email', needsTracking: true, isReply: false },
  { value: 'replied', label: 'Replied', needsTracking: false, isReply: true },
  { value: 'not_replied', label: 'Did not reply', needsTracking: false, isReply: true },
  { value: 'always', label: 'Always (no condition — jump to a step)', needsTracking: false, isReply: false },
]

export function conditionMeta(condition: StepBranchCondition): ConditionMeta {
  const meta = CONDITIONS.find((entry) => entry.value === condition)
  if (!meta) throw new Error(`unknown condition ${condition}`)
  return meta
}

/**
 * The editor's state. Strings throughout because they are form values: `''`
 * means "none" — no label narrowing, or an exit that ends the path.
 */
export type ConditionDraft = {
  condition: StepBranchCondition
  withinDays: string
  replyLabelKey: string
  yesStepId: string
  noStepId: string
}

/** A new condition starts as the reference flow's first one: opened within 3 days. */
export function draftFromBranch(branch: StepBranch | null): ConditionDraft {
  if (!branch) {
    return { condition: 'opened', withinDays: String(DEFAULT_WITHIN_DAYS), replyLabelKey: '', yesStepId: '', noStepId: '' }
  }
  return {
    condition: branch.condition,
    withinDays: branch.within_days === null ? String(DEFAULT_WITHIN_DAYS) : String(branch.within_days),
    replyLabelKey: branch.reply_label_key ?? '',
    yesStepId: branch.yes_step_id ?? '',
    noStepId: branch.no_step_id ?? '',
  }
}

/** A reply label as the editor needs it. */
export type RouteableLabel = { key: string; label: string; stopsEnrollment: boolean }

export type DraftContext = {
  /** The step the condition hangs off. */
  stepId: string
  /** Every step of the campaign — exits may only name one of these. */
  stepIds: ReadonlySet<string>
  /** `undefined` while unknown: the check is then left to the server. */
  trackingEnabled: boolean | undefined
  /** The step and every A/B variant carry an HTML body; `undefined` while unknown. */
  htmlEverywhere: boolean | undefined
  /** The workspace's reply labels; `undefined` while loading. */
  replyLabels: readonly RouteableLabel[] | undefined
}

export type DraftField = 'condition' | 'withinDays' | 'replyLabel' | 'yes' | 'no'
export type DraftProblem = { field: DraftField; message: string }

export const TRACKING_REQUIRED_HINT =
  'Opens and clicks are only recorded when tracking is on for this campaign and this step — and each of its A/B variants — has an HTML body.'

/** Everything that would make the server refuse this draft, per field. */
export function validateDraft(draft: ConditionDraft, context: DraftContext): DraftProblem[] {
  const problems: DraftProblem[] = []
  const meta = conditionMeta(draft.condition)

  if (draft.condition !== 'always') {
    const days = Number(draft.withinDays)
    if (!Number.isInteger(days) || days < MIN_WITHIN_DAYS || days > MAX_WITHIN_DAYS) {
      problems.push({
        field: 'withinDays',
        message: `A whole number of days from ${MIN_WITHIN_DAYS} to ${MAX_WITHIN_DAYS}.`,
      })
    }
  }

  if (meta.needsTracking) {
    if (context.trackingEnabled === false) {
      problems.push({
        field: 'condition',
        message: `Tracking is off for this campaign — turn it on from the Overview tab. ${TRACKING_REQUIRED_HINT}`,
      })
    } else if (context.htmlEverywhere === false) {
      problems.push({ field: 'condition', message: `This step has no HTML body. ${TRACKING_REQUIRED_HINT}` })
    }
  }

  if (meta.isReply && draft.replyLabelKey !== '' && context.replyLabels) {
    const label = context.replyLabels.find((entry) => entry.key === draft.replyLabelKey)
    if (!label) {
      problems.push({ field: 'replyLabel', message: 'That reply label no longer exists.' })
    } else if (label.stopsEnrollment) {
      problems.push({
        field: 'replyLabel',
        message: `Replies labelled “${label.label}” stop the sequence, so they can never be routed.`,
      })
    }
  }

  const exits: [DraftField, string][] = [['yes', draft.yesStepId]]
  if (draft.condition !== 'always') exits.push(['no', draft.noStepId])
  for (const [field, target] of exits) {
    if (target === '') continue
    if (target === context.stepId) {
      problems.push({ field, message: 'A step can’t route back to itself — that would be a loop.' })
    } else if (!context.stepIds.has(target)) {
      problems.push({ field, message: 'That step no longer exists.' })
    }
  }
  return problems
}

/**
 * The request for a draft. Fields the condition doesn't use are sent as null
 * rather than whatever the form still holds, so switching a reply condition to
 * "opened" can't smuggle a label along, and "always" never carries a window or
 * a no exit.
 *
 * `basedOn` is the concurrency token: the `updated_at` of the branch the draft
 * was built from, sent back verbatim (never through a Date — the server's
 * token has microseconds, and a millisecond round-trip never matches), or
 * null for a new condition, which the server then applies only if the step
 * still has none. A mismatch is a 409 `branch_changed`.
 */
export function toBranchRequest(draft: ConditionDraft, basedOn: string | null): StepBranchRequest {
  const always = draft.condition === 'always'
  const meta = conditionMeta(draft.condition)
  return {
    condition: draft.condition,
    within_days: always ? null : Number(draft.withinDays),
    reply_label_key: meta.isReply && draft.replyLabelKey !== '' ? draft.replyLabelKey : null,
    yes_step_id: draft.yesStepId === '' ? null : draft.yesStepId,
    no_step_id: always || draft.noStepId === '' ? null : draft.noStepId,
    expected_updated_at: basedOn,
  }
}

/**
 * The request that changes one exit of an existing branch and keeps the rest —
 * conditional on that branch still being the one the drag was made against.
 */
export function withExit(branch: StepBranch, exit: 'yes' | 'no', target: string): StepBranchRequest {
  return {
    expected_updated_at: branch.updated_at,
    condition: branch.condition,
    within_days: branch.within_days,
    reply_label_key: branch.reply_label_key,
    yes_step_id: exit === 'yes' ? target : branch.yes_step_id,
    no_step_id: exit === 'no' ? target : branch.no_step_id,
  }
}

const SUMMARY: Record<StepBranchCondition, string> = {
  opened: 'Opened',
  not_opened: 'Not opened',
  clicked: 'Clicked',
  replied: 'Replied',
  not_replied: 'No reply',
  always: 'Always',
}

/** The condition node's text: "Opened within 3 days", "Replied · Interested within 5 days". */
export function describeBranch(branch: Pick<StepBranch, 'condition' | 'within_days' | 'reply_label_key'>, labelName?: string): string {
  if (branch.condition === 'always') return 'Always'
  const narrowed = branch.reply_label_key ? ` · ${labelName ?? branch.reply_label_key}` : ''
  const days = branch.within_days ?? 0
  return `${SUMMARY[branch.condition]}${narrowed} within ${days} day${days === 1 ? '' : 's'}`
}
