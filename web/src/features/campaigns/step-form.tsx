import { useId, useState } from 'react'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RichTextEditor, SubjectEditor } from '@/components/shared/rich-text-editor'
import { unknownTokens } from '@/components/shared/rich-text/merge-tags'
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
import { useMergeFields } from './merge-fields'
import { UnknownMergeFieldsNotice } from './unknown-merge-fields'

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
  body_html: z.string().optional(),
})
const firstStepSchema = followUpSchema.extend({
  subject: z.string().min(1, 'Required').max(500, 'Max 500 characters'),
})
type Values = z.infer<typeof followUpSchema>

/**
 * Inline add/edit form for a sequence step. Collects the delay as whole
 * days + hours (converted to `delay_seconds`), a required subject, and an
 * optional body. Subject and body are merge-field-aware editors: a placeholder
 * nothing will fill in is flagged in place and blocks the save. `create`
 * appends at the end; `edit` targets an existing step and allows the delay to
 * change. Edit is available in any campaign status
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
  /** Called with the saved step, so a caller that just created one can place it (the API appends at the end). */
  onDone: (saved?: SequenceStep) => void
  onCancel: () => void
}) {
  const isEdit = step != null
  const [createStep, createState] = useCreateStepMutation()
  const [updateStep, updateState] = useUpdateStepMutation()
  const isSaving = createState.isLoading || updateState.isLoading
  const error = createState.error ?? updateState.error

  const daysId = useId()
  const hoursId = useId()
  const subjectLabelId = useId()
  const subjectHintId = useId()
  const bodyLabelId = useId()
  const bodyHintId = useId()
  const unknownId = useId()
  const mergeFields = useMergeFields()
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
    control,
    setValue,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<Values>({
    resolver: zodResolver(isFirstStep ? firstStepSchema : followUpSchema),
    defaultValues: {
      days: initialDelay.days,
      hours: initialDelay.hours,
      subject: step?.subject ?? '',
      body_text: step?.body_text ?? '',
      body_html: step?.body_html ?? '',
    },
  })
  const copy = useWatch({ control, name: ['subject', 'body_text', 'body_html'] })
  const unknown = unknownTokens(
    copy.map((template) => template ?? ''),
    mergeFields.classify,
  )

  async function onSubmit(values: Values) {
    // The notice above the buttons already says why; saving would only move
    // the failure to send time, where it is permanent.
    if (unknown.length > 0) return
    // `secondsToDelay` floors to whole days+hours, so a delay carrying a
    // sub-hour remainder would round-trip lossily. Keep the original seconds
    // untouched unless the user actually changed the day/hour inputs.
    const delaySecondsUnchanged = values.days === initialDelay.days && values.hours === initialDelay.hours
    const stepRequest = {
      delay_seconds: delaySecondsUnchanged ? originalDelaySeconds : delayToSeconds(values.days, values.hours),
      subject: values.subject,
      body_text: values.body_text ?? '',
      body_html: values.body_html ?? '',
    }
    if (isEdit && step?.id) {
      const result = await updateStep({ id: campaignId, stepId: step.id, stepRequest })
      if ('data' in result) onDone(result.data)
      return
    }
    const result = await createStep({ id: campaignId, stepRequest })
    if ('data' in result) onDone(result.data)
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
          <Label id={subjectLabelId}>Subject</Label>
          {!isFirstStep && <span className="text-[12px] text-muted-foreground">optional</span>}
        </div>
        <Controller
          control={control}
          name="subject"
          render={({ field }) => (
            <SubjectEditor
              labelledBy={subjectLabelId}
              describedBy={subjectHintId}
              initialText={field.value}
              placeholder={isFirstStep ? 'Quick question, {{first_name}}' : 'Leave blank to stay in the thread'}
              invalid={!!errors.subject}
              variables={mergeFields.variables}
              classifyVariable={mergeFields.classify}
              onChange={field.onChange}
            />
          )}
        />
        {errors.subject ? (
          <span id={subjectHintId} className="text-xs text-danger">
            {errors.subject.message}
          </span>
        ) : (
          !isFirstStep && (
            <span id={subjectHintId} className="text-xs text-muted-foreground">
              Blank sends this step as a reply in the same thread — the subject becomes “Re:” the first
              step's subject.
            </span>
          )
        )}
      </div>

      <div className="flex flex-col gap-1.5">
        <Label id={bodyLabelId}>Body</Label>
        <RichTextEditor
          labelledBy={bodyLabelId}
          describedBy={bodyHintId}
          initialHtml={step?.body_html ?? ''}
          initialText={step?.body_text ?? ''}
          placeholder={'Hi {{first_name}},\n\n…'}
          variables={mergeFields.variables}
          classifyVariable={mergeFields.classify}
          onChange={(body) => {
            setValue('body_text', body.text)
            setValue('body_html', body.html)
          }}
        />
        <span id={bodyHintId} className="text-xs text-muted-foreground">
          Type {'{{'} to insert a merge field — each one is filled in per contact. {'{option a|option b}'} spins a
          random variant per send.
        </span>
      </div>

      <UnknownMergeFieldsNotice id={unknownId} names={unknown} />

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
        {/* `isSubmitting` covers the validation tick before the mutation
            starts, so a quick double click can't create the step twice. */}
        <Button type="submit" variant="primary" size="sm" disabled={isSaving || isSubmitting}>
          {isSaving && <Loader2 className="animate-spin" />}
          {isEdit ? 'Save step' : 'Add step'}
        </Button>
      </div>
    </form>
  )
}
