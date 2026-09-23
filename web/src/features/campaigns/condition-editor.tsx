import { useId, useMemo, useState } from 'react'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
// Read-only cross-feature hook (the documented exception): which reply labels
// exist, and which of them stop the sequence, belongs to the reply-labels
// feature. Hooks only — no reply-labels UI or state is imported.
import { useListReplyLabelsQuery } from '@/features/reply-labels/api'
import {
  useDeleteStepBranchMutation,
  useGetCampaignQuery,
  useListStepVariantsQuery,
  useSetStepBranchMutation,
  type StepBranch,
} from './api'
import {
  CONDITIONS,
  MAX_WITHIN_DAYS,
  MIN_WITHIN_DAYS,
  conditionMeta,
  draftFromBranch,
  toBranchRequest,
  validateDraft,
  type ConditionDraft,
  type DraftField,
  type RouteableLabel,
} from './branch-draft'
import { branchErrorMessage } from './branch-error'
import { cycleStepIds } from './branch-loop'
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
  onSaved,
  onDeleted,
  onCancel,
  onLoop,
}: ConditionEditorProps) {
  const [draft, setDraft] = useState<ConditionDraft>(() => draftFromBranch(branch))
  const [save, saveState] = useSetStepBranchMutation()
  const [remove, removeState] = useDeleteStepBranchMutation()
  const busy = saveState.isLoading || removeState.isLoading
  const failure = saveState.error ?? removeState.error

  const { data: campaign } = useGetCampaignQuery({ id: campaignId })
  const { data: variants } = useListStepVariantsQuery({ id: campaignId, stepId: step.id })
  const labelsQuery = useListReplyLabelsQuery()
  const labels = useMemo<RouteableLabel[] | undefined>(
    () =>
      labelsQuery.data?.labels.map((label) => ({
        key: label.key,
        label: label.label,
        stopsEnrollment: label.stops_enrollment,
      })),
    [labelsQuery.data],
  )

  const stepIds = useMemo(() => new Set(steps.map((candidate) => candidate.id)), [steps])
  const problems = validateDraft(draft, {
    stepId: step.id,
    stepIds,
    trackingEnabled: campaign?.tracking_enabled,
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
    if (problems.length > 0 || busy) return
    onLoop(null)
    const result = await save({ id: campaignId, stepId: step.id, stepBranchRequest: toBranchRequest(draft) })
    if ('error' in result) {
      onLoop(cycleStepIds(result.error))
      return
    }
    onSaved()
  }

  async function onRemove() {
    onLoop(null)
    const result = await remove({ id: campaignId, stepId: step.id })
    if ('error' in result) {
      onLoop(cycleStepIds(result.error))
      return
    }
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
          hint={replyLabelHint(stoppingCount, labelsQuery.isError)}
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

      {failure && (
        <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
          {branchErrorMessage(failure)}
        </p>
      )}

      <div className="flex items-center gap-2">
        {branch && (
          <Button type="button" variant="ghost" size="sm" className="text-danger" disabled={busy} onClick={() => void onRemove()}>
            {removeState.isLoading && <Loader2 className="animate-spin" />}
            Remove condition
          </Button>
        )}
        <span className="ml-auto" />
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" size="sm" disabled={busy || problems.length > 0}>
          {saveState.isLoading && <Loader2 className="animate-spin" />}
          {branch ? 'Save condition' : 'Add condition'}
        </Button>
      </div>
    </form>
  )
}

function hasHtml(body: string | undefined): boolean {
  return (body ?? '').trim() !== ''
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
      <span id={hintId} className={problem ? 'text-xs text-danger' : 'text-xs text-muted-foreground'}>
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
