import { useId, useMemo, useState } from 'react'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
import {
  useDeleteStepBranchMutation,
  useListStepVariantsQuery,
  useSetStepBranchMutation,
  type StepBranch,
} from './api'
import {
  CONDITIONS,
  MAX_WITHIN_DAYS,
  MIN_WITHIN_DAYS,
  conditionMeta,
  describeBranch,
  draftFromBranch,
  toBranchRequest,
  validateDraft,
  type ConditionDraft,
  type DraftField,
} from './branch-draft'
import { branchErrorMessage, changedBranch } from './branch-error'
import { cycleStepIds } from './branch-loop'
import { useBranchRules } from './branch-rules'
import type { StepWithId } from './step-card'

export type ConditionEditorProps = {
  campaignId: string
  step: StepWithId
  /** 1-based. */
  position: number
  /** Every step in order — the exits can name any of them but this one. */
  steps: readonly StepWithId[]
  /** The router already on this step, or null when adding one. */
  branch: StepBranch | null
  /** Start from this draft instead of `branch` — a drag the rules refused, shown with its problem. */
  initialDraft?: ConditionDraft
  /** The routing failed to reload: nothing can be saved against it safely. */
  routingUnavailable: boolean
  /** Reports each successful write (null = removed) so the canvas shows it before the refetch lands. */
  onWritten: (branch: StepBranch | null) => void
  onSaved: () => void
  onDeleted: () => void
  onCancel: () => void
  /** The loop a refused write named, or `null` to clear the highlight. */
  onLoop: (stepIds: string[] | null) => void
}

/**
 * The router on one step: what to watch for, for how long, and where each
 * answer goes. Branch writes are allowed on a running campaign — they change
 * routing, not the steps — so nothing here is gated on status.
 *
 * Every rule the server enforces is checked here first (`validateDraft`) and
 * explained next to the field it concerns; the server's typed error still has
 * the last word for what the client can't know.
 */
export function ConditionEditor({
  campaignId,
  step,
  position,
  steps,
  branch,
  initialDraft,
  routingUnavailable,
  onWritten,
  onSaved,
  onDeleted,
  onCancel,
  onLoop,
}: ConditionEditorProps) {
  const [draft, setDraft] = useState<ConditionDraft>(() => initialDraft ?? draftFromBranch(branch))
  // Two versions, kept apart on purpose:
  // - `basedOn` is the branch the draft is built from. It is the concurrency
  //   token every write sends (`expected_updated_at`, verbatim; null = this is
  //   a new condition), so the server refuses the write if the branch has moved
  //   on since — even from another tab this one never heard about.
  // - `observed` is the latest version this editor has seen. When it moves on
  //   (an exit dragged on the canvas, a refetch, or a 409 reporting the
  //   server's current branch), an untouched form follows it — and rebases its
  //   token. An edited one keeps the user's draft and its old token, and says
  //   so: the user reviews the change before anything overwrites it.
  // Adjusted during render (React's pattern for derived state), not an effect.
  const version = branch?.updated_at ?? null
  const [observed, setObserved] = useState(version)
  const [basedOn, setBasedOn] = useState(version)
  const [baseline, setBaseline] = useState<ConditionDraft>(() => draftFromBranch(branch))
  const [changedElsewhere, setChangedElsewhere] = useState(false)
  const [save, saveState] = useSetStepBranchMutation()
  const [remove, removeState] = useDeleteStepBranchMutation()
  if (version !== observed) {
    setObserved(version)
    if (!changedElsewhere && sameDraft(draft, baseline)) {
      const latest = draftFromBranch(branch)
      setDraft(latest)
      setBaseline(latest)
      setBasedOn(version)
    } else {
      setChangedElsewhere(true)
    }
  }

  function loadLatest() {
    const latest = draftFromBranch(branch)
    setDraft(latest)
    setBaseline(latest)
    setBasedOn(version)
    setChangedElsewhere(false)
    saveState.reset()
    removeState.reset()
  }

  const busy = saveState.isLoading || removeState.isLoading
  const failure = saveState.error ?? removeState.error

  const rules = useBranchRules(campaignId)
  const { data: variants } = useListStepVariantsQuery({ id: campaignId, stepId: step.id })
  const labels = rules.replyLabels

  const stepIds = useMemo(() => new Set(steps.map((candidate) => candidate.id)), [steps])
  const problems = validateDraft(draft, {
    stepId: step.id,
    stepIds,
    trackingEnabled: rules.trackingEnabled,
    htmlEverywhere:
      variants === undefined ? undefined : hasHtml(step.body_html) && variants.every((variant) => hasHtml(variant.body_html)),
    replyLabels: labels,
  })
  const problemFor = (field: DraftField) => problems.find((problem) => problem.field === field)?.message

  const meta = conditionMeta(draft.condition)
  const always = draft.condition === 'always'
  const routeable = labels?.filter((label) => !label.stopsEnrollment) ?? []
  const stoppingCount = (labels?.length ?? 0) - routeable.length
  const exitOptions = steps.filter((candidate) => candidate.id !== step.id)

  const ids = {
    condition: useId(),
    days: useId(),
    label: useId(),
    yes: useId(),
    no: useId(),
    conditionHint: useId(),
    daysHint: useId(),
    labelHint: useId(),
    yesHint: useId(),
    noHint: useId(),
  }

  function update(patch: Partial<ConditionDraft>) {
    setDraft((current) => ({ ...current, ...patch }))
  }

  async function onSubmit(event: React.FormEvent) {
    event.preventDefault()
    if (problems.length > 0 || busy || routingUnavailable || changedElsewhere) return
    onLoop(null)
    const result = await save({ id: campaignId, stepId: step.id, stepBranchRequest: toBranchRequest(draft, basedOn) })
    if ('error' in result) {
      onLoop(cycleStepIds(result.error))
      // Someone else's write got there first. Show theirs — never retry ours
      // over it: the user hasn't seen what they'd be replacing.
      const conflict = changedBranch(result.error)
      if (conflict) onWritten(conflict.current)
      return
    }
    onWritten(result.data)
    onSaved()
  }

  async function onRemove() {
    if (routingUnavailable || changedElsewhere || basedOn === null) return
    onLoop(null)
    const result = await remove({ id: campaignId, stepId: step.id, expectedUpdatedAt: basedOn })
    if ('error' in result) {
      onLoop(cycleStepIds(result.error))
      const conflict = changedBranch(result.error)
      // Already removed elsewhere: what the user asked for is true. Done.
      if (conflict?.current === null) {
        onWritten(null)
        onDeleted()
        return
      }
      if (conflict) onWritten(conflict.current)
      return
    }
    onWritten(null)
    onDeleted()
  }

  return (
    <form onSubmit={(event) => void onSubmit(event)} noValidate className="grid gap-4 p-5">
      <p className="text-xs text-muted-foreground">
        Decides where someone goes after step {position} is sent. The window counts from that send, and only human
        opens and clicks count. The next step’s own wait starts once the answer is known.
      </p>

      <Field
        id={ids.condition}
        label="If they…"
        hintId={ids.conditionHint}
        problem={problemFor('condition')}
      >
        <Select
          id={ids.condition}
          value={draft.condition}
          aria-invalid={problemFor('condition') ? true : undefined}
          aria-describedby={ids.conditionHint}
          onChange={(event) => {
            // Narrow the DOM string back to the contract's union via the list
            // the options were rendered from, rather than asserting it.
            const next = CONDITIONS.find((option) => option.value === event.target.value)
            if (next) update({ condition: next.value })
          }}
        >
          {CONDITIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </Field>

      {!always && (
        <Field
          id={ids.days}
          label="Within (days)"
          hintId={ids.daysHint}
          problem={problemFor('withinDays')}
          hint={
            draft.condition.startsWith('not_')
              ? 'They take the Yes path once this many days pass without it; No as soon as it happens.'
              : 'They take the Yes path as soon as it happens; No once this many days pass without it.'
          }
        >
          <Input
            id={ids.days}
            type="number"
            inputMode="numeric"
            min={MIN_WITHIN_DAYS}
            max={MAX_WITHIN_DAYS}
            className="w-28"
            value={draft.withinDays}
            aria-invalid={problemFor('withinDays') ? true : undefined}
            aria-describedby={ids.daysHint}
            onChange={(event) => update({ withinDays: event.target.value })}
          />
        </Field>
      )}

      {meta.isReply && (
        <Field
          id={ids.label}
          label="With the reply label"
          hintId={ids.labelHint}
          problem={problemFor('replyLabel')}
          hint={replyLabelHint(stoppingCount, rules.labelsFailed)}
        >
          <Select
            id={ids.label}
            value={draft.replyLabelKey}
            aria-invalid={problemFor('replyLabel') ? true : undefined}
            aria-describedby={ids.labelHint}
            onChange={(event) => update({ replyLabelKey: event.target.value })}
          >
            <option value="">Any reply that doesn’t stop the sequence</option>
            {draft.replyLabelKey !== '' && !routeable.some((label) => label.key === draft.replyLabelKey) && (
              // The saved label was deleted or now stops the sequence; keep it
              // selectable so the form shows what is saved, and flag it above.
              <option value={draft.replyLabelKey}>
                {labels?.find((label) => label.key === draft.replyLabelKey)?.label ?? draft.replyLabelKey} (can’t route)
              </option>
            )}
            {routeable.map((label) => (
              <option key={label.key} value={label.key}>
                {label.label}
              </option>
            ))}
          </Select>
        </Field>
      )}

      <ExitSelect
        id={ids.yes}
        hintId={ids.yesHint}
        label={always ? 'Then go to' : 'Yes — go to'}
        value={draft.yesStepId}
        options={exitOptions}
        allSteps={steps}
        problem={problemFor('yes')}
        onChange={(yesStepId) => update({ yesStepId })}
      />
      {!always && (
        <ExitSelect
          id={ids.no}
          hintId={ids.noHint}
          label="No — go to"
          value={draft.noStepId}
          options={exitOptions}
          allSteps={steps}
          problem={problemFor('no')}
          onChange={(noStepId) => update({ noStepId })}
        />
      )}
      <p className="text-xs text-muted-foreground">
        You can also drag from the condition’s {always ? 'exit' : 'Yes or No handle'} onto a step.
      </p>

      {changedElsewhere && (
        <div role="status" className="flex flex-wrap items-center gap-2 rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn">
          <span className="min-w-0 flex-1">
            This condition was changed elsewhere since you started editing
            {branch ? ` — it’s now “${describeBranch(branch)}”` : ' — it has been removed'}. Load the latest to
            continue from it; your edits here would replace a version you haven’t seen, so they can’t be saved.
          </span>
          <Button type="button" variant="ghost" size="xs" onClick={loadLatest}>
            Load the latest
          </Button>
        </div>
      )}
      {routingUnavailable && (
        <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
          Couldn’t reload this sequence’s conditions, so saving is held — it could replace a change you can’t see.
          Retry from the banner above; your edits stay here.
        </p>
      )}
      {failure && (
        <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
          {branchErrorMessage(failure)}
        </p>
      )}

      <div className="flex items-center gap-2">
        {basedOn !== null && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="text-danger"
            disabled={busy || routingUnavailable || changedElsewhere}
            onClick={() => void onRemove()}
          >
            {removeState.isLoading && <Loader2 className="animate-spin" />}
            Remove condition
          </Button>
        )}
        <span className="ml-auto" />
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
        <Button
          type="submit"
          variant="primary"
          size="sm"
          disabled={busy || routingUnavailable || changedElsewhere || problems.length > 0}
        >
          {saveState.isLoading && <Loader2 className="animate-spin" />}
          {basedOn !== null ? 'Save condition' : 'Add condition'}
        </Button>
      </div>
    </form>
  )
}

/**
 * Exactly the server's test (`checkTrackable`: `BodyHtml == ""`), untrimmed —
 * a whitespace-only body passes there, so it passes here too.
 */
function sameDraft(a: ConditionDraft, b: ConditionDraft): boolean {
  return (
    a.condition === b.condition &&
    a.withinDays === b.withinDays &&
    a.replyLabelKey === b.replyLabelKey &&
    a.yesStepId === b.yesStepId &&
    a.noStepId === b.noStepId
  )
}

function hasHtml(body: string | undefined): boolean {
  return (body ?? '') !== ''
}

function replyLabelHint(stoppingCount: number, failed: boolean): string {
  const rule =
    'A reply whose label stops the sequence — every default label does — ends it before this condition is checked, so only labels that don’t stop it can be routed on. To route on one, turn off “Stops the sequence” for it in Settings → Reply labels.'
  if (failed) return `Couldn’t load reply labels, so only “any reply” is offered. ${rule}`
  if (stoppingCount === 0) return rule
  return `${stoppingCount} label${stoppingCount === 1 ? '' : 's'} stop${stoppingCount === 1 ? 's' : ''} the sequence and ${stoppingCount === 1 ? 'isn’t' : 'aren’t'} offered. ${rule}`
}

/** One labelled control, its hint, and — when the draft breaks a rule — why, in words. */
function Field({
  id,
  label,
  hint,
  hintId,
  problem,
  children,
}: {
  id: string
  label: string
  hint?: string
  hintId: string
  problem: string | undefined
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {/* Polite: a problem appears as the user types, and should be heard without interrupting them. */}
      <span id={hintId} aria-live="polite" className={problem ? 'text-xs text-danger' : 'text-xs text-muted-foreground'}>
        {problem ?? hint}
      </span>
    </div>
  )
}

function ExitSelect({
  id,
  hintId,
  label,
  value,
  options,
  allSteps,
  problem,
  onChange,
}: {
  id: string
  hintId: string
  label: string
  value: string
  options: readonly StepWithId[]
  allSteps: readonly StepWithId[]
  problem: string | undefined
  onChange: (stepId: string) => void
}) {
  return (
    <Field id={id} label={label} hintId={hintId} problem={problem}>
      <Select
        id={id}
        value={value}
        aria-invalid={problem ? true : undefined}
        aria-describedby={hintId}
        onChange={(event) => onChange(event.target.value)}
      >
        <option value="">End the sequence</option>
        {options.map((option) => (
          <option key={option.id} value={option.id}>
            {stepOptionLabel(option, allSteps)}
          </option>
        ))}
      </Select>
    </Field>
  )
}

function stepOptionLabel(step: StepWithId, steps: readonly StepWithId[]): string {
  const position = steps.findIndex((candidate) => candidate.id === step.id) + 1
  const subject = step.subject?.trim() || (position > 1 ? 'same-thread follow-up' : 'no subject yet')
  return `Step ${position} — ${subject}`
}
