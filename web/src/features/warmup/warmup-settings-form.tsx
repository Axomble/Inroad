import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { httpStatus } from '@/lib/rtk-error'
import type { WarmupParticipant } from '@/store/api'
import { useEnableMailboxWarmupMutation } from './api'
import {
  warmupSettingsSchema,
  warmupSettingsDefaults,
  type WarmupSettingsValues,
} from './settings-schema'

/** Human copy for a failed enable/update, narrowed via the typed rtk-error helper. */
function saveErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === 400) return 'Those settings were rejected — check the ranges and try again.'
  if (status === 404) return 'This mailbox no longer exists — refresh the page.'
  return "Couldn't save warmup settings. Please try again."
}

/**
 * Enable warmup on a mailbox, or update an existing participant's ramp. One
 * form for both: the same `PUT /mailboxes/{id}/warmup` is idempotent, so
 * prefilling from an existing participant turns "enable" into "update".
 * Validation runs client-side (matching the contract) before the request, and
 * the server's own boundary validation is surfaced inline on failure.
 */
export function WarmupSettingsForm({
  mailboxId,
  participant,
  onDone,
  onCancel,
}: {
  mailboxId: string
  /** Existing enrollment to prefill; omitted when enabling for the first time. */
  participant?: WarmupParticipant
  onDone: () => void
  onCancel: () => void
}) {
  const [enable, { isLoading, error }] = useEnableMailboxWarmupMutation()
  const startId = useId()
  const maxId = useId()
  const incId = useId()
  const replyId = useId()

  const {
    register,
    handleSubmit,
    watch,
    formState: { errors },
  } = useForm<WarmupSettingsValues>({
    resolver: zodResolver(warmupSettingsSchema),
    defaultValues: participant
      ? {
          start_volume: participant.start_volume,
          max_volume: participant.max_volume,
          ramp_increment: participant.ramp_increment,
          reply_rate: participant.reply_rate,
        }
      : warmupSettingsDefaults,
  })

  // Live echo of the reply-rate decimal as a percentage so the 0–1 field reads
  // clearly ("0.3 = 30% of sends are replies").
  const replyRate = watch('reply_rate')
  const replyPct = Number.isFinite(Number(replyRate))
    ? `${Math.round(Number(replyRate) * 100)}%`
    : '—'

  async function onSubmit(values: WarmupSettingsValues) {
    const result = await enable({ id: mailboxId, warmupSettings: values })
    if ('data' in result && result.data) onDone()
  }

  return (
    <form onSubmit={handleSubmit(onSubmit)} noValidate className="grid gap-4 border-t border-border bg-surface/40 p-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field id={startId} label="Start volume" hint="emails/day at day 0" error={errors.start_volume?.message}>
          <Input id={startId} type="number" inputMode="numeric" min={1} max={200} aria-invalid={!!errors.start_volume} {...register('start_volume', { valueAsNumber: true })} />
        </Field>
        <Field id={maxId} label="Max volume" hint="daily ceiling (≤ 200)" error={errors.max_volume?.message}>
          <Input id={maxId} type="number" inputMode="numeric" min={1} max={200} aria-invalid={!!errors.max_volume} {...register('max_volume', { valueAsNumber: true })} />
        </Field>
        <Field id={incId} label="Ramp increment" hint="emails/day added each day" error={errors.ramp_increment?.message}>
          <Input id={incId} type="number" inputMode="numeric" min={1} aria-invalid={!!errors.ramp_increment} {...register('ramp_increment', { valueAsNumber: true })} />
        </Field>
        <Field id={replyId} label="Reply rate" hint={`share of sends that reply (0–1) · ${replyPct}`} error={errors.reply_rate?.message}>
          <Input id={replyId} type="number" inputMode="decimal" step={0.05} min={0} max={1} aria-invalid={!!errors.reply_rate} {...register('reply_rate', { valueAsNumber: true })} />
        </Field>
      </div>

      {error && (
        <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
          {saveErrorMessage(error)}
        </p>
      )}

      <div className="flex items-center justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" variant="warm" size="sm" disabled={isLoading}>
          {isLoading && <Loader2 className="animate-spin" />}
          {participant ? 'Save settings' : 'Enable warmup'}
        </Button>
      </div>
    </form>
  )
}

/** A labelled numeric field row with hint + inline validation message. */
function Field({
  id,
  label,
  hint,
  error,
  children,
}: {
  id: string
  label: string
  hint: string
  error?: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error ? (
        <span className="text-xs text-danger">{error}</span>
      ) : (
        <span className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">{hint}</span>
      )}
    </div>
  )
}
