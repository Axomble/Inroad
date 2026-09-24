import { useId } from 'react'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
import { RichTextEditor, SubjectEditor } from '@/components/shared/rich-text-editor'
import { unknownTokens } from '@/components/shared/rich-text/merge-tags'
import { httpStatus } from '@/lib/rtk-error'
import { useCreateCampaignMutation } from './api'
import { useMergeFields } from './merge-fields'
import { UnknownMergeFieldsNotice } from './unknown-merge-fields'
// Cross-feature query-hook imports are allowed for read-only reference data
// (see features/campaigns/api.ts). Cross-feature UI imports remain forbidden.
import { useListMailboxesQuery } from '@/features/mailboxes/api'
import { useListListsQuery } from '@/features/contacts/api'

const schema = z.object({
  name: z.string().min(1, 'Required'),
  mailbox_id: z.string().uuid('Select a mailbox'),
  list_id: z.string().uuid('Select a list'),
  subject: z.string().min(1, 'Required'),
  body_text: z.string().optional(),
  body_html: z.string().optional(),
})
type Values = z.infer<typeof schema>

function createErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === 422) return 'That mailbox is not connected or active.'
  if (status === 404) return 'That list no longer exists.'
  if (status === 400) return 'Please fill in all required fields.'
  return "Couldn't create the campaign. Please try again."
}

export function CampaignForm({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const { data: mailboxes = [] } = useListMailboxesQuery()
  const { data: lists = [] } = useListListsQuery()
  const [create, { isLoading, error }] = useCreateCampaignMutation()
  const nameId = useId()
  const mailboxId = useId()
  const listId = useId()
  const subjectLabelId = useId()
  const bodyLabelId = useId()
  const bodyHintId = useId()
  const unknownId = useId()
  const mergeFields = useMergeFields()

  const {
    register,
    control,
    setValue,
    handleSubmit,
    formState: { errors },
  } = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { subject: '', body_text: '', body_html: '' } })
  const copy = useWatch({ control, name: ['subject', 'body_text', 'body_html'] })
  const unknown = unknownTokens(
    copy.map((template) => template ?? ''),
    mergeFields.classify,
  )

  const activeMailboxes = mailboxes.filter((m) => m.status === 'active')

  async function onSubmit(values: Values) {
    // The campaign's copy becomes its first step, so the step form's rule
    // applies: a placeholder nothing fills in is not saved.
    if (unknown.length > 0) return
    const result = await create({ createCampaignRequest: values })
    if ('data' in result && result.data) onDone()
  }

  return (
    <div className="border-b border-border bg-surface/40">
      <div className="flex h-10 items-center border-b border-border px-5">
        <span className="font-mono text-[10.5px] uppercase tracking-[0.14em] text-faint">New campaign</span>
      </div>
      <form onSubmit={handleSubmit(onSubmit)} noValidate className="grid gap-4 p-5">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={nameId}>Name</Label>
          <Input id={nameId} placeholder="Q3 outreach" aria-invalid={!!errors.name} {...register('name')} />
          {errors.name && <span className="text-xs text-danger">{errors.name.message}</span>}
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={mailboxId}>Send from</Label>
            <Select id={mailboxId} aria-invalid={!!errors.mailbox_id} {...register('mailbox_id')}>
              <option value="">Select a mailbox…</option>
              {activeMailboxes.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.email}
                </option>
              ))}
            </Select>
            {errors.mailbox_id && <span className="text-xs text-danger">{errors.mailbox_id.message}</span>}
            {activeMailboxes.length === 0 && (
              <span className="text-xs text-muted-foreground">No active mailboxes — connect one first.</span>
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={listId}>To list</Label>
            <Select id={listId} aria-invalid={!!errors.list_id} {...register('list_id')}>
              <option value="">Select a list…</option>
              {lists.map((l) => (
                <option key={l.id} value={l.id}>
                  {l.name}
                </option>
              ))}
            </Select>
            {errors.list_id && <span className="text-xs text-danger">{errors.list_id.message}</span>}
          </div>
        </div>

        <div className="flex flex-col gap-1.5">
          <Label id={subjectLabelId}>Subject</Label>
          <Controller
            control={control}
            name="subject"
            render={({ field }) => (
              <SubjectEditor
                labelledBy={subjectLabelId}
                initialText={field.value}
                placeholder="Quick question, {{first_name}}"
                invalid={!!errors.subject}
                variables={mergeFields.variables}
                classifyVariable={mergeFields.classify}
                onChange={field.onChange}
              />
            )}
          />
          {errors.subject && <span className="text-xs text-danger">{errors.subject.message}</span>}
        </div>

        <div className="flex flex-col gap-1.5">
          <Label id={bodyLabelId}>Body</Label>
          <RichTextEditor
            labelledBy={bodyLabelId}
            describedBy={bodyHintId}
            placeholder={'Hi {{first_name}},\n\n…'}
            variables={mergeFields.variables}
            classifyVariable={mergeFields.classify}
            onChange={(body) => {
              setValue('body_text', body.text)
              setValue('body_html', body.html)
            }}
          />
          <span id={bodyHintId} className="font-mono text-[10px] uppercase tracking-[0.12em] text-faint">
            Type {'{{'} to insert a merge field
          </span>
        </div>

        <UnknownMergeFieldsNotice id={unknownId} names={unknown} />

        {error && (
          <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
            {createErrorMessage(error)}
          </p>
        )}

        <div className="flex items-center justify-end gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" size="sm" disabled={isLoading}>
            {isLoading && <Loader2 className="animate-spin" />}
            Create campaign
          </Button>
        </div>
      </form>
    </div>
  )
}
