import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { useAppSelector } from '@/store/hooks'
// The email-verification gate is the auth feature's own concern — it owns both
// the state and the copy, so a call site names only the action.
import { VerifiedGateButton } from '@/features/auth/verified-gate-button'
import {
  useCreateStepMutation,
  useUpdateStepMutation,
  useTestSendCampaignMutation,
  type SequenceStep,
} from './api'
import { delayToSeconds, secondsToDelay } from './step-delay'
import { TEST_SEND_GATED_ACTION, stepErrorMessage, testSendErrorMessage } from './step-error'

/**
 * Two schemas, not one: the first step's subject opens the thread, so it is
 * required; a follow-up may leave it blank, which the sender turns into a
 * same-thread reply ("Re: <step 1 subject>"). The blank is a feature, not a
 * validation gap.
 */
const followUpSchema = z.object({
  days: z.number({ message: 'Number' }).int().min(0, '0+').max(365, 'Max 365'),
  hours: z.number({ message: 'Number' }).int().min(0, '0+').max(23, 'Max 23'),
  subject: z.string().max(500, 'Max 500 characters'),
  body_text: z.string().optional(),
})
const firstStepSchema = followUpSchema.extend({
  subject: z.string().min(1, 'Required').max(500, 'Max 500 characters'),
})
type Values = z.infer<typeof followUpSchema>

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
  isFirstStep,
  onDone,
  onCancel,
}: {
  campaignId: string
  /** Present in edit mode; absent when adding a new step. */
  step?: SequenceStep
  /** First step opens the thread, so its subject is required. */
  isFirstStep: boolean
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
  const testAddressId = useId()

  // Defaults to the signed-in operator's own inbox — the most common test
  // recipient — but stays editable so a teammate's mailbox can be checked too.
  const userEmail = useAppSelector((s) => s.auth.userEmail)
  const [testSend, testSendState] = useTestSendCampaignMutation()
  const [testAddress, setTestAddress] = useState(userEmail ?? '')
  const [sentTo, setSentTo] = useState<string | null>(null)

  async function onSendTest() {
    const stepId = step?.id
    const address = testAddress.trim()
    if (!stepId || !address) return
    setSentTo(null)
    const result = await testSend({ id: campaignId, testSendRequest: { step_id: stepId, to: address } })
    if ('data' in result) setSentTo(address)
  }

  const originalDelaySeconds = step?.delay_seconds ?? 0
  const initialDelay = secondsToDelay(originalDelaySeconds)
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<Values>({
    resolver: zodResolver(isFirstStep ? firstStepSchema : followUpSchema),
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
        <div className="flex items-center gap-2">
          <Label htmlFor={subjectId}>Subject</Label>
          {!isFirstStep && (
            <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-faint">optional</span>
          )}
        </div>
        <Input
          id={subjectId}
          placeholder={isFirstStep ? 'Quick question, {{first_name}}' : 'Leave blank to stay in the thread'}
          aria-invalid={!!errors.subject}
          {...register('subject')}
        />
        {errors.subject ? (
          <span className="text-xs text-danger">{errors.subject.message}</span>
        ) : (
          !isFirstStep && (
            <span className="text-xs text-muted-foreground">
              Blank sends this step as a reply in the same thread — the subject becomes “Re:” the first
              step's subject.
            </span>
          )
        )}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label htmlFor={bodyId}>Body</Label>
        <Textarea
          id={bodyId}
          rows={5}
          placeholder={'Hi {{first_name}},\n\n…'}
          {...register('body_text')}
        />
        <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-faint">
          {'{{first_name}}'} and {'{{email}}'} are personalized per contact — {'{option a|option b}'}{' '}
          spins a random variant per send
        </span>
      </div>

      {error && (
        <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
          {stepErrorMessage(error)}
        </p>
      )}

      {/* Edit-mode only: a step being added has nothing rendered yet to test
          send, and no `step.id` for the API to key the test-send request on. */}
      {isEdit && step?.id && (
        <div className="flex flex-wrap items-end gap-3 border-t border-border pt-4">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={testAddressId}>Send test to</Label>
            <Input
              id={testAddressId}
              type="email"
              className="w-56"
              value={testAddress}
              onChange={(e) => {
                setTestAddress(e.target.value)
                setSentTo(null)
              }}
            />
          </div>
          {/* POST /campaigns/{id}/test-send is behind `auth.RequireVerified`.
              Saving the step itself is not, so Save stays enabled. */}
          <VerifiedGateButton
            action={TEST_SEND_GATED_ACTION}
            type="button"
            variant="ghost"
            size="sm"
            disabled={testSendState.isLoading || testAddress.trim() === ''}
            onClick={() => void onSendTest()}
          >
            {testSendState.isLoading && <Loader2 className="animate-spin" />}
            Send test
          </VerifiedGateButton>
          {sentTo && (
            <p role="status" className="text-xs text-ok">
              Test queued for {sentTo} — it should arrive shortly.
            </p>
          )}
          {testSendState.error && (
            <p role="alert" className="text-xs text-danger">
              {testSendErrorMessage(testSendState.error)}
            </p>
          )}
        </div>
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
