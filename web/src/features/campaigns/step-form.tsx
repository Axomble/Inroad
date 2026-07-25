import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'
import { useCreateStepMutation, useUpdateStepMutation, type SequenceStep } from './api'
import { delayToSeconds, secondsToDelay } from './step-delay'
import { stepErrorMessage } from './step-error'

const schema = z.object({
  days: z.number({ message: 'Number' }).int().min(0, '0+').max(365, 'Max 365'),
  hours: z.number({ message: 'Number' }).int().min(0, '0+').max(23, 'Max 23'),
  subject: z.string().min(1, 'Required'),
  body_text: z.string().optional(),
})
type Values = z.infer<typeof schema>

const field =
  'h-9 w-full rounded-md border border-border-strong bg-surface-2 px-3 text-[13px] text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring'

/**
 * Inline add/edit form for a sequence step. Collects the delay as whole
 * days + hours (converted to `delay_seconds`), a required subject, and an
 * optional body. `create` appends at the end; `edit` targets an existing step
 * and allows the delay to change. Edit is available in any campaign status
 * (content is live-reference); add is draft-only, enforced by the caller.
 */
export function StepForm({
  campaignId,
  step,
  onDone,
  onCancel,
}: {
  campaignId: string
  /** Present in edit mode; absent when adding a new step. */
  step?: SequenceStep
  onDone: () => void
  onCancel: () => void
}) {
  const isEdit = step != null
  const [createStep, createState] = useCreateStepMutation()
  const [updateStep, updateState] = useUpdateStepMutation()
  const isSaving = createState.isLoading || updateState.isLoading
  const error = createState.error ?? updateState.error

  const daysId = useId()
  const hoursId = useId()
  const subjectId = useId()
  const bodyId = useId()

  const originalDelaySeconds = step?.delay_seconds ?? 0
  const initialDelay = secondsToDelay(originalDelaySeconds)
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: {
      days: initialDelay.days,
      hours: initialDelay.hours,
      subject: step?.subject ?? '',
      body_text: step?.body_text ?? '',
    },
  })

  async function onSubmit(values: Values) {
    // `secondsToDelay` floors to whole days+hours, so a delay carrying a
    // sub-hour remainder would round-trip lossily. Keep the original seconds
    // untouched unless the user actually changed the day/hour inputs.
    const delaySecondsUnchanged = values.days === initialDelay.days && values.hours === initialDelay.hours
    const stepRequest = {
      delay_seconds: delaySecondsUnchanged ? originalDelaySeconds : delayToSeconds(values.days, values.hours),
      subject: values.subject,
      body_text: values.body_text ?? '',
    }
    if (isEdit && step?.id) {
      const result = await updateStep({ id: campaignId, stepId: step.id, stepRequest })
      if ('data' in result) onDone()
      return
    }
    const result = await createStep({ id: campaignId, stepRequest })
    if ('data' in result) onDone()
  }

  return (
    <form onSubmit={handleSubmit(onSubmit)} noValidate className="grid gap-4 border-b border-border bg-surface/60 p-5">
      <div className="flex items-center gap-2">
        <span className="font-mono text-[10.5px] uppercase tracking-[0.14em] text-faint">
          {isEdit ? 'Edit step' : 'Add step'}
        </span>
      </div>

      <div className="flex flex-wrap items-end gap-4">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={daysId}>Delay · days</Label>
          <Input
            id={daysId}
            type="number"
            min={0}
            max={365}
            className="w-24"
            aria-invalid={!!errors.days}
            {...register('days', { valueAsNumber: true })}
          />
          {errors.days && <span className="text-xs text-danger">{errors.days.message}</span>}
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={hoursId}>Hours</Label>
          <Input
            id={hoursId}
            type="number"
            min={0}
            max={23}
            className="w-24"
            aria-invalid={!!errors.hours}
            {...register('hours', { valueAsNumber: true })}
          />
          {errors.hours && <span className="text-xs text-danger">{errors.hours.message}</span>}
        </div>
        <p className="pb-2 text-xs text-muted-foreground">Wait after the previous step before sending this one.</p>
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={subjectId}>Subject</Label>
        <Input
          id={subjectId}
          placeholder="Quick question, {{first_name}}"
          aria-invalid={!!errors.subject}
          {...register('subject')}
        />
        {errors.subject && <span className="text-xs text-danger">{errors.subject.message}</span>}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={bodyId}>Body</Label>
        <textarea
          id={bodyId}
          rows={5}
          placeholder={'Hi {{first_name}},\n\n…'}
          className={cn(field, 'h-auto resize-y py-2 leading-relaxed')}
          {...register('body_text')}
        />
        <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-faint">
          {'{{first_name}}'} and {'{{email}}'} are personalized per contact
        </span>
      </div>

      {error && (
        <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
          {stepErrorMessage(error)}
        </p>
      )}

      <div className="flex items-center justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" size="sm" disabled={isSaving}>
          {isSaving && <Loader2 className="animate-spin" />}
          {isEdit ? 'Save step' : 'Add step'}
        </Button>
      </div>
    </form>
  )
}
