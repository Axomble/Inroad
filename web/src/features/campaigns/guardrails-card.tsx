import { useEffect, useState } from 'react'
import { AlertCircle, ShieldAlert } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { SectionBar } from '@/components/layout/page'
import { StatusPill } from '@/components/shared/status-pill'
import { cn } from '@/lib/utils'
import {
  MAX_THRESHOLD_PCT,
  MIN_THRESHOLD_PCT,
  autoPauseCopy,
  guardrailsErrorMessage,
  pauseEventSentence,
  pauseReasonLabel,
  reportErrorMessage,
  thresholdFromDraft,
  thresholdToDraft,
  verdictCopy,
} from '@/lib/deliverability-copy'
import { useGetCampaignDeliverabilityQuery, useUpdateCampaignGuardrailsMutation } from './api'
import type { CampaignGuardrails, CampaignPauseEvent } from './api'

/**
 * The circuit breaker for one campaign: its two thresholds, whether it is armed,
 * the verdict right now, and every automatic pause spelled out.
 *
 * The pause list is the reason this card exists. A campaign that stopped itself
 * with no explanation is worse than one that never stopped, so each event is
 * rendered as a full sentence carrying the rate, the threshold and the sample it
 * was judged on (`lib/deliverability-copy`, which both this card and the
 * Deliverability page share).
 */
export function GuardrailsCard({ campaignId }: { campaignId: string }) {
  const { data, isLoading, error } = useGetCampaignDeliverabilityQuery({ id: campaignId })
  const [save, { isLoading: isSaving, error: saveError }] = useUpdateCampaignGuardrailsMutation()

  const [bounce, setBounce] = useState('')
  const [complaint, setComplaint] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  const [dirty, setDirty] = useState(false)

  // Seed from the server once, and re-seed after a save. Guarded on `dirty` so a
  // background refetch can't discard an edit in progress.
  useEffect(() => {
    if (!data || dirty) return
    setBounce(thresholdToDraft(data.guardrails.bounce_pause_pct))
    setComplaint(thresholdToDraft(data.guardrails.complaint_pause_pct))
  }, [data, dirty])

  /**
   * Both controls submit the whole object, because the API's PUT is a full
   * replace. Validating the drafts on every submit — including the toggle's — is
   * what stops flipping the switch from quietly persisting a stale threshold
   * while the operator looks at an edited one.
   */
  async function submit(autoPauseEnabled: boolean) {
    const parsedBounce = thresholdFromDraft(bounce, 'bounce')
    if ('problem' in parsedBounce) {
      setProblem(parsedBounce.problem)
      return
    }
    const parsedComplaint = thresholdFromDraft(complaint, 'complaint')
    if ('problem' in parsedComplaint) {
      setProblem(parsedComplaint.problem)
      return
    }
    setProblem(null)
    const body: CampaignGuardrails = {
      auto_pause_enabled: autoPauseEnabled,
      bounce_pause_pct: parsedBounce.pct,
      complaint_pause_pct: parsedComplaint.pct,
    }
    const result = await save({ id: campaignId, campaignGuardrails: body })
    // A failure is surfaced from `saveError` below; the draft stays dirty so the
    // operator's values aren't thrown away by the refetch.
    if (!('error' in result)) setDirty(false)
  }

  if (isLoading) {
    return (
      <Shell>
        <div className="space-y-2 px-4 py-4 sm:px-5">
          <Skeleton className="h-4 w-52" />
          <Skeleton className="h-3 w-72" />
        </div>
      </Shell>
    )
  }

  // A failed load must not render as a campaign with no guardrails and no pauses:
  // that reads as "nothing has ever gone wrong", which is a claim we can't make.
  if (error || !data) {
    return (
      <Shell>
        <p role="alert" className="flex items-start gap-2 px-4 py-4 text-sm text-danger sm:px-5">
          <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          <span>{reportErrorMessage(error)}</span>
        </p>
      </Shell>
    )
  }

  const verdict = verdictCopy(data.verdict, data.guardrails)
  const autoPause = autoPauseCopy(data.guardrails)
  const enabled = data.guardrails.auto_pause_enabled

  function editThreshold(setter: (value: string) => void, value: string) {
    setter(value)
    setDirty(true)
    setProblem(null)
  }

  return (
    <Shell
      actions={
        dirty && (
          <Button size="xs" disabled={isSaving} onClick={() => void submit(enabled)}>
            {isSaving ? 'Saving…' : 'Save thresholds'}
          </Button>
        )
      }
    >
      <div className="space-y-4 px-4 py-4 sm:px-5">
        {/* The verdict. `warn` is its own label, tone and sentence — the one state
            where the operator still has time to act. */}
        <div
          data-verdict={data.verdict}
          className={cn(
            'rounded-lg border px-3.5 py-3',
            data.verdict === 'paused' && 'border-danger/40 bg-danger/10',
            data.verdict === 'warn' && 'border-warn/40 bg-warn/10',
            data.verdict === 'ok' && 'border-border bg-surface-2/60',
          )}
        >
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <StatusPill tone={verdict.tone}>{verdict.label}</StatusPill>
            {verdict.actionable && (
              <span className="inline-flex items-center gap-1 font-mono text-[10.5px] uppercase tracking-[0.1em] text-warn">
                <ShieldAlert className="size-3.5" aria-hidden="true" />
                Act now
              </span>
            )}
          </div>
          <p className="mt-1.5 text-xs leading-relaxed text-muted-foreground">{verdict.detail}</p>
        </div>

        <div className="flex flex-wrap items-start gap-x-6 gap-y-3">
          <div className="space-y-1">
            <span className="flex items-center gap-2">
              <button
                type="button"
                role="switch"
                aria-checked={enabled}
                aria-label={enabled ? 'Turn automatic pausing off' : 'Turn automatic pausing on'}
                disabled={isSaving}
                onClick={() => void submit(!enabled)}
                className={cn(
                  'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50',
                  enabled ? 'bg-ok' : 'bg-border-strong',
                )}
              >
                <span
                  className={cn(
                    'inline-block size-3.5 rounded-full bg-background shadow transition-transform',
                    enabled ? 'translate-x-[18px]' : 'translate-x-1',
                  )}
                />
              </button>
              <StatusPill tone={autoPause.tone} dot={false}>
                {autoPause.label}
              </StatusPill>
            </span>
            <p className="max-w-sm text-xs leading-relaxed text-muted-foreground">{autoPause.detail}</p>
          </div>

          {/* Bottom-aligned: "Complaint threshold" wraps to two lines on a narrow
              viewport, which would otherwise leave the two inputs on different
              baselines. */}
          <div className="flex flex-wrap items-end gap-4">
            <ThresholdField
              id="guardrail-bounce"
              label="Bounce threshold"
              value={bounce}
              onChange={(value) => editThreshold(setBounce, value)}
            />
            <ThresholdField
              id="guardrail-complaint"
              label="Complaint threshold"
              value={complaint}
              onChange={(value) => editThreshold(setComplaint, value)}
            />
          </div>
        </div>

        {problem && (
          <p role="alert" className="text-sm text-danger">
            {problem}
          </p>
        )}
        {saveError && (
          <p role="alert" className="text-sm text-danger">
            {guardrailsErrorMessage(saveError)}
          </p>
        )}

        <PauseEvents events={data.pause_events} />
      </div>
    </Shell>
  )
}

function Shell({ actions, children }: { actions?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section aria-label="Deliverability guardrails" className="border-b border-border">
      <SectionBar label="Deliverability guardrails">{actions}</SectionBar>
      {children}
    </section>
  )
}

function ThresholdField({
  id,
  label,
  value,
  onChange,
}: {
  id: string
  label: string
  value: string
  onChange: (value: string) => void
}) {
  return (
    <div className="w-32 space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <div className="flex items-center gap-1.5">
        <Input
          id={id}
          type="number"
          inputMode="decimal"
          step="0.1"
          min={MIN_THRESHOLD_PCT}
          max={MAX_THRESHOLD_PCT}
          className="h-8"
          value={value}
          onChange={(event) => onChange(event.target.value)}
        />
        <span className="font-mono text-xs text-faint">%</span>
      </div>
    </div>
  )
}

/**
 * Every automatic pause, in full. Never a bare "paused": the sentence carries the
 * metric, the observed rate, the threshold it crossed and the sample it was judged
 * on, so the operator knows what to fix before restarting.
 */
function PauseEvents({ events }: { events: CampaignPauseEvent[] }) {
  if (events.length === 0) {
    return (
      <p className="text-xs text-muted-foreground">
        This campaign has never been paused automatically.
      </p>
    )
  }
  return (
    <div className="space-y-1.5">
      <h4 className="font-mono text-[10.5px] uppercase tracking-[0.12em] text-faint">
        Automatic pauses ({events.length})
      </h4>
      <ul className="space-y-1.5">
        {events.map((event) => (
          <li
            key={`${event.created_at}-${event.metric}`}
            className="rounded-md border border-danger/30 bg-danger/5 px-3 py-2"
          >
            <StatusPill tone="failing">{pauseReasonLabel(event)}</StatusPill>
            <p className="mt-1 text-xs leading-relaxed text-foreground">{pauseEventSentence(event)}</p>
          </li>
        ))}
      </ul>
    </div>
  )
}
