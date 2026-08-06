import { memo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { z } from 'zod'
import { ArrowLeft, CalendarClock, Loader2, MessageSquareText, NotebookPen, RotateCcw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { EmptyBlock, Page, PageBody, PageTopbar, Stat, StatStrip } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import {
  useCrmCreateNoteMutation,
  useCrmCreateTaskMutation,
  useCrmGetDealQuery,
  useCrmListDealThreadsQuery,
  useCrmListEventsQuery,
  useCrmListNotesQuery,
  useCrmListTasksQuery,
  useCrmMoveDealMutation,
  type CrmEvent,
} from './api'
import { parseActor } from './actor'
import { ActorBadge } from './actor-badge'
import { crmErrorMessage } from './error-copy'
import { formatMoney } from './money'

const noteSchema = z.object({
  title: z.string().trim().max(200),
  body: z.string().trim().min(1, 'Add a note before saving.').max(20_000),
})
type NoteValues = z.infer<typeof noteSchema>

const taskSchema = z.object({
  title: z.string().trim().min(1, 'Add a task title.').max(200),
  body: z.string().trim().max(20_000),
  dueAt: z.string(),
})
type TaskValues = z.infer<typeof taskSchema>

const stageChangeDataSchema = z.object({ from_stage_id: z.string().uuid() }).passthrough()

/** The API's page cap; see the pagination note in crm-page.tsx. */
const attachmentPageSize = 200

export function DealDetailPage({ dealId }: { dealId: string }) {
  const dealQuery = useCrmGetDealQuery({ id: dealId })
  const eventsQuery = useCrmListEventsQuery({ targetType: 'deal', targetId: dealId })
  const threadsQuery = useCrmListDealThreadsQuery({ id: dealId })
  // Notes and tasks are keyset-paginated; ask for the server's cap so a busy
  // deal's history isn't quietly cut off at the default page size.
  const notesQuery = useCrmListNotesQuery({ targetType: 'deal', targetId: dealId, limit: attachmentPageSize })
  const tasksQuery = useCrmListTasksQuery({ targetType: 'deal', targetId: dealId, limit: attachmentPageSize })
  const deal = dealQuery.data

  if (dealQuery.isLoading) {
    return <Page><PageBody><div className="h-72 animate-pulse rounded-xl bg-surface-2" aria-label="Loading deal" /></PageBody></Page>
  }
  // A failed request is not the same as a missing deal: a 500 or an offline
  // browser must offer a retry, not tell the user their deal was deleted.
  if (dealQuery.isError && httpStatus(dealQuery.error) !== 404) {
    return (
      <Page>
        <PageBody>
          <EmptyBlock
            title="This deal could not be loaded"
            description={crmErrorMessage(dealQuery.error, 'Try again in a moment.')}
            action={<Button onClick={() => void dealQuery.refetch()} disabled={dealQuery.isFetching}>Try again</Button>}
          />
        </PageBody>
      </Page>
    )
  }
  if (!deal) {
    return (
      <Page>
        <PageBody>
          <EmptyBlock
            title="Deal not found"
            description="It may have been removed or belong to another workspace."
            action={<Button asChild><Link to="/app/deals">Back to deals</Link></Button>}
          />
        </PageBody>
      </Page>
    )
  }

  const openTasks = tasksQuery.data?.items.filter(({ status }) => status === 'open' || status === 'in_progress') ?? []
  return (
    <Page>
      <PageTopbar
        eyebrow="Deal"
        title={deal.name}
        subtitle={deal.company_name || deal.contact_email || 'Unlinked opportunity'}
        actions={
          <>
            {/* Who created this deal, next to the deal itself — the activity
                feed below only attributes individual events. */}
            <ActorBadge actor={parseActor(deal.created_by_actor)} source={deal.source} />
            <Button asChild size="sm"><Link to="/app/deals"><ArrowLeft aria-hidden="true" />Board</Link></Button>
          </>
        }
      />
      <StatStrip>
        <Stat label="Value" value={formatMoney(deal.amount_micros ?? 0, deal.currency)} sub={deal.currency} />
        <Stat label="Stage" value={deal.stage_label} sub={deal.pipeline_name} />
        <Stat label="Next actions" value={openTasks.length} sub="Open or in progress" />
      </StatStrip>
      <PageBody>
        <div className="grid min-w-0 gap-5 xl:grid-cols-[minmax(0,1.45fr)_minmax(18rem,0.75fr)]">
          <div className="min-w-0 space-y-5">
            <Section title="What happened" description="A chronological, attributed record. Repeated events within ten minutes are grouped.">
              {eventsQuery.isLoading ? <InlineLoading /> : null}
              {eventsQuery.data?.items.length ? (
                <ol className="divide-y divide-border">
                  {eventsQuery.data.items.map((event) => <ActivityRow key={event.id} dealId={dealId} event={event} />)}
                </ol>
              ) : !eventsQuery.isLoading ? <MutedEmpty text="No activity has been recorded yet." /> : null}
            </Section>
            <Section title="Conversation context" description="Structured headers and participants only; inbound message bodies are not exposed.">
              {threadsQuery.isLoading ? <InlineLoading /> : null}
              <div className="space-y-3">
                {threadsQuery.data?.items.map((thread) => (
                  <article key={thread.id} className="rounded-lg border border-border bg-background p-4">
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <div>
                        <h3 className="text-sm font-semibold">{thread.subject || 'Email thread'}</h3>
                        <p className="mt-1 text-xs text-muted-foreground">{thread.participants.map(({ email }) => email).join(', ')}</p>
                      </div>
                      {thread.reply_class ? <span className="rounded-full bg-ok/10 px-2 py-1 text-xs font-medium text-ok">{thread.reply_class}</span> : null}
                    </div>
                    <ol className="mt-3 space-y-2">
                      {thread.messages.map((message) => (
                        <li key={message.id} className="flex items-center gap-2 text-xs text-muted-foreground">
                          <MessageSquareText className="size-3.5" aria-hidden="true" />
                          <span className="font-medium text-foreground">{message.direction === 'inbound' ? 'Reply received' : 'Message sent'}</span>
                          <time dateTime={message.occurred_at}>{formatDate(message.occurred_at)}</time>
                        </li>
                      ))}
                    </ol>
                  </article>
                ))}
              </div>
              {!threadsQuery.isLoading && !threadsQuery.data?.items.length ? <MutedEmpty text="No campaign thread is linked to this deal." /> : null}
            </Section>
            <Section title="Notes" description="Shared context for people and agents working this opportunity.">
              <NoteComposer dealId={dealId} />
              <div className="mt-4 space-y-2">
                {notesQuery.data?.items.map((note) => (
                  <article key={note.id} className="rounded-lg border border-border bg-background p-3">
                    {note.title ? <h3 className="text-sm font-semibold">{note.title}</h3> : null}
                    <p className="mt-1 whitespace-pre-wrap text-sm text-muted-foreground">{note.body}</p>
                    <p className="mt-2 flex flex-wrap items-center gap-2 text-xs text-faint">
                      <ActorBadge actor={parseActor(note.created_by_actor)} />
                      <time dateTime={note.created_at}>{formatDate(note.created_at)}</time>
                    </p>
                  </article>
                ))}
                {notesQuery.data?.next_cursor != null ? <MoreExist noun="notes" /> : null}
              </div>
            </Section>
          </div>
          <aside className="min-w-0 space-y-5">
            <Section title="What matters now" description="The core context for this opportunity.">
              <dl className="grid gap-3 text-sm">
                <Detail label="Company" value={deal.company_name || 'Not linked'} />
                <Detail label="Primary contact" value={deal.contact_email || 'Not linked'} />
                <Detail label="Source" value={deal.source === 'reply' ? 'Positive campaign reply' : deal.source} />
                <Detail label="Close date" value={deal.close_date ? formatDate(deal.close_date) : 'Not set'} />
              </dl>
            </Section>
            <Section title="What's next" description="Concrete follow-up work, visible to the whole workspace.">
              <TaskComposer dealId={dealId} />
              <ul className="mt-4 space-y-2">
                {openTasks.map((task) => (
                  <li key={task.id} className="rounded-lg border border-border bg-background p-3">
                    <div className="flex items-start gap-2">
                      <CalendarClock className="mt-0.5 size-4 text-accent-ink" aria-hidden="true" />
                      <div className="min-w-0">
                        <p className="text-sm font-medium">{task.title}</p>
                        <p className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                          <ActorBadge actor={parseActor(task.created_by_actor)} />
                          {task.due_at ? <time dateTime={task.due_at}>Due {formatDate(task.due_at)}</time> : null}
                        </p>
                      </div>
                    </div>
                  </li>
                ))}
                {tasksQuery.data?.next_cursor != null ? <MoreExist noun="tasks" /> : null}
              </ul>
              {!tasksQuery.isLoading && openTasks.length === 0 ? <MutedEmpty text="No open tasks. Add the next concrete action." /> : null}
            </Section>
          </aside>
        </div>
      </PageBody>
    </Page>
  )
}

function NoteComposer({ dealId }: { dealId: string }) {
  const [createNote, state] = useCrmCreateNoteMutation()
  const form = useForm<NoteValues>({ resolver: zodResolver(noteSchema), defaultValues: { title: '', body: '' } })
  const submit = form.handleSubmit(async ({ title, body }) => {
    try {
      await createNote({ crmNoteInput: { title, body, target_type: 'deal', target_id: dealId } }).unwrap()
      form.reset()
    } catch (error) {
      form.setError('root', { message: crmErrorMessage(error, 'The note could not be saved. Try again.') })
    }
  })
  return (
    <form onSubmit={(event) => void submit(event)} className="grid gap-2">
      <Label htmlFor="note-title">Title <span className="text-muted-foreground">(optional)</span></Label>
      <Input id="note-title" {...form.register('title')} />
      <Label htmlFor="note-body">Note</Label>
      <textarea
        id="note-body"
        rows={3}
        className="w-full resize-y rounded-md border border-input bg-surface-2 p-2.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
        aria-invalid={Boolean(form.formState.errors.body)}
        aria-describedby={form.formState.errors.body ? 'note-error' : undefined}
        {...form.register('body')}
      />
      {form.formState.errors.body ? <p id="note-error" className="text-xs text-danger">{form.formState.errors.body.message}</p> : null}
      {form.formState.errors.root ? <p role="alert" className="text-xs text-danger">{form.formState.errors.root.message}</p> : null}
      <Button type="submit" size="sm" className="justify-self-start" disabled={state.isLoading}>
        {state.isLoading ? <Loader2 className="animate-spin" aria-hidden="true" /> : <NotebookPen aria-hidden="true" />}
        Save note
      </Button>
    </form>
  )
}

function TaskComposer({ dealId }: { dealId: string }) {
  const [createTask, state] = useCrmCreateTaskMutation()
  const form = useForm<TaskValues>({ resolver: zodResolver(taskSchema), defaultValues: { title: '', body: '', dueAt: '' } })
  const submit = form.handleSubmit(async ({ title, body, dueAt }) => {
    try {
      await createTask({
        crmTaskInput: {
          title,
          body,
          // `datetime-local` yields a local wall-clock string; the API takes an
          // instant, so it is resolved against the browser's zone here.
          due_at: dueAt ? new Date(dueAt).toISOString() : undefined,
          status: 'open',
          target_type: 'deal',
          target_id: dealId,
        },
      }).unwrap()
      form.reset()
    } catch (error) {
      form.setError('root', { message: crmErrorMessage(error, 'The task could not be saved. Try again.') })
    }
  })
  return (
    <form onSubmit={(event) => void submit(event)} className="grid gap-2">
      <Label htmlFor="task-title">Next action</Label>
      <Input id="task-title" placeholder="Book a discovery call" aria-invalid={Boolean(form.formState.errors.title)} {...form.register('title')} />
      {form.formState.errors.title ? <p className="text-xs text-danger">{form.formState.errors.title.message}</p> : null}
      <Label htmlFor="task-due">Due date <span className="text-muted-foreground">(optional)</span></Label>
      <Input id="task-due" type="datetime-local" {...form.register('dueAt')} />
      <input type="hidden" {...form.register('body')} />
      {form.formState.errors.root ? <p role="alert" className="text-xs text-danger">{form.formState.errors.root.message}</p> : null}
      <Button type="submit" variant="primary" size="sm" className="justify-self-start" disabled={state.isLoading}>
        {state.isLoading ? <Loader2 className="animate-spin" aria-hidden="true" /> : <CalendarClock aria-hidden="true" />}
        Add task
      </Button>
    </form>
  )
}

const ActivityRow = memo(function ActivityRow({ dealId, event }: { dealId: string; event: CrmEvent }) {
  const [moveDeal, moveState] = useCrmMoveDealMutation()
  const [revertError, setRevertError] = useState<string | null>(null)
  const label = event.name.split('.').map((part) => part.replaceAll('_', ' ')).join(' ')
  // Actor/data are open JSON objects in the API contract. Parse that boundary
  // once before using fields in labels or mutations.
  const actor = parseActor(event.actor)
  const previousStage = event.name === 'deal.stage_changed' ? stageChangeDataSchema.safeParse(event.data) : null
  const canRevert = previousStage?.success === true

  const revert = async () => {
    if (!canRevert) return
    setRevertError(null)
    try {
      await moveDeal({ id: dealId, crmMoveDealInput: { stage_id: previousStage.data.from_stage_id } }).unwrap()
    } catch (error) {
      setRevertError(crmErrorMessage(error, 'The stage change could not be reverted.'))
    }
  }

  return (
    <li className="flex min-w-0 gap-3 py-3 first:pt-0 last:pb-0">
      <span className="mt-1.5 size-2 shrink-0 rounded-full bg-primary" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0">
            <p className="text-sm font-medium capitalize">{label}</p>
            <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
              <ActorBadge actor={actor} />
              {event.merged_count && event.merged_count > 1 ? <span>{event.merged_count} grouped events</span> : null}
              <time dateTime={event.occurred_at}>{formatDate(event.occurred_at)}</time>
            </p>
          </div>
          {canRevert ? (
            <Button type="button" variant="outline" size="sm" onClick={() => void revert()} disabled={moveState.isLoading} aria-label="Revert this stage change">
              {moveState.isLoading ? <Loader2 className="animate-spin" aria-hidden="true" /> : <RotateCcw aria-hidden="true" />}
              Revert
            </Button>
          ) : null}
        </div>
        {event.source_thread_ref || event.source_message_id ? (
          <p className="mt-2 break-all text-xs text-muted-foreground">
            Source: {event.source_thread_ref ? `thread ${event.source_thread_ref}` : ''}
            {event.source_thread_ref && event.source_message_id ? ' / ' : ''}
            {event.source_message_id ? `message ${event.source_message_id}` : ''}
          </p>
        ) : null}
        {actor.type === 'agent' && actor.thread_id ? <p className="mt-1 break-all text-xs text-faint">Agent thread {actor.thread_id}{actor.run_id ? ` / run ${actor.run_id}` : ''}</p> : null}
        {revertError ? <p role="alert" className="mt-2 text-xs text-danger">{revertError}</p> : null}
      </div>
    </li>
  )
})

function Section({ title, description, children }: { title: string; description: string; children: React.ReactNode }) {
  return (
    <section className="min-w-0 rounded-xl border border-border bg-surface p-4 sm:p-5">
      <header className="mb-4">
        <h2 className="text-base font-semibold">{title}</h2>
        <p className="mt-1 text-sm text-muted-foreground">{description}</p>
      </header>
      {children}
    </section>
  )
}

function Detail({ label, value }: { label: string; value: string }) {
  return <div><dt className="text-xs font-medium text-muted-foreground">{label}</dt><dd className="mt-0.5 break-words font-medium">{value}</dd></div>
}
function InlineLoading() { return <p className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" aria-hidden="true" />Loading</p> }
function MutedEmpty({ text }: { text: string }) { return <p className="rounded-lg border border-dashed border-border p-4 text-sm text-muted-foreground">{text}</p> }
/** The page cap was reached — say so rather than let the list read as whole. */
function MoreExist({ noun }: { noun: string }) { return <p role="status" className="pt-1 text-xs text-muted-foreground">Older {noun} are not shown.</p> }
function formatDate(value: string) { return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) }
