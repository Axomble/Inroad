import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'
import { useCreateCampaignMutation, useListMailboxesQuery, useListListsQuery } from './api'

const schema = z.object({
  name: z.string().min(1, 'Required'),
  mailbox_id: z.string().uuid('Select a mailbox'),
  list_id: z.string().uuid('Select a list'),
  subject: z.string().min(1, 'Required'),
  body_text: z.string().optional(),
})
type Values = z.infer<typeof schema>

function createErrorMessage(error: unknown): string {
  const status = (error as { status?: number })?.status
  if (status === 422) return 'That mailbox is not connected or active.'
  if (status === 404) return 'That list no longer exists.'
  if (status === 400) return 'Please fill in all required fields.'
  return "Couldn't create the campaign. Please try again."
}

const field = 'h-9 w-full rounded-md border border-border-strong bg-surface-2 px-3 text-[13px] text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring'

export function CampaignForm({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const { data: mailboxes = [] } = useListMailboxesQuery()
  const { data: lists = [] } = useListListsQuery()
  const [create, { isLoading, error }] = useCreateCampaignMutation()
  const nameId = useId()
  const mailboxId = useId()
  const listId = useId()
  const subjectId = useId()
  const bodyId = useId()

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<Values>({ resolver: zodResolver(schema) })

  const activeMailboxes = mailboxes.filter((m) => m.status === 'active')

  async function onSubmit(values: Values) {
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
            <select id={mailboxId} className={cn(field)} aria-invalid={!!errors.mailbox_id} {...register('mailbox_id')}>
              <option value="">Select a mailbox…</option>
              {activeMailboxes.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.email}
                </option>
              ))}
            </select>
            {errors.mailbox_id && <span className="text-xs text-danger">{errors.mailbox_id.message}</span>}
            {activeMailboxes.length === 0 && (
              <span className="text-xs text-muted-foreground">No active mailboxes — connect one first.</span>
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={listId}>To list</Label>
            <select id={listId} className={cn(field)} aria-invalid={!!errors.list_id} {...register('list_id')}>
              <option value="">Select a list…</option>
              {lists.map((l) => (
                <option key={l.id} value={l.id}>
                  {l.name}
                </option>
              ))}
            </select>
            {errors.list_id && <span className="text-xs text-danger">{errors.list_id.message}</span>}
          </div>
        </div>

        <div className="flex flex-col gap-1.5">
          <Label htmlFor={subjectId}>Subject</Label>
          <Input id={subjectId} placeholder="Quick question, {{first_name}}" aria-invalid={!!errors.subject} {...register('subject')} />
          {errors.subject && <span className="text-xs text-danger">{errors.subject.message}</span>}
        </div>

        <div className="flex flex-col gap-1.5">
          <Label htmlFor={bodyId}>Body</Label>
          <textarea
            id={bodyId}
            rows={6}
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
