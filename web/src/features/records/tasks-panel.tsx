import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { z } from 'zod'
import { CalendarClock, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useCrmCreateTaskMutation, type CrmTargetType } from './api'
import { recordErrorMessage } from './error-copy'
import { formatDateTime } from '@/lib/datetime'
import { parseActor } from './actor'
import { ActorBadge } from './actor-badge'
import { InlineLoading, MoreExist, MutedEmpty, QueryErrorBanner, Section } from '@/components/shared/record-page'
import { useOpenTasks } from './use-open-tasks'

const taskSchema = z.object({
  title: z.string().trim().min(1, 'Add a task title.').max(200),
  body: z.string().trim().max(20_000),
  dueAt: z.string(),
})
type TaskValues = z.infer<typeof taskSchema>

/** The open follow-up work on any CRM record, and the form that adds to it. */
export function TasksPanel({ targetType, targetId }: { targetType: CrmTargetType; targetId: string }) {
  const { query, open } = useOpenTasks(targetType, targetId)

  return (
    <Section title="What's next" description="Concrete follow-up work, visible to the whole workspace.">
      <TaskComposer targetType={targetType} targetId={targetId} />
      {query.isLoading ? <div className="mt-4"><InlineLoading label="Loading tasks" /></div> : null}
      {query.isError ? (
        <QueryErrorBanner
          className="mt-4"
          message={recordErrorMessage(query.error, 'Tasks could not be loaded.')}
          onRetry={() => void query.refetch()}
          retrying={query.isFetching}
        />
      ) : null}
      <ul className="mt-4 space-y-2">
        {open.map((task) => (
          <li key={task.id} className="rounded-lg border border-border bg-background p-3">
            <div className="flex items-start gap-2">
              <CalendarClock className="mt-0.5 size-4 text-accent-ink" aria-hidden="true" />
              <div className="min-w-0">
                <p className="text-sm font-medium">{task.title}</p>
                <p className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  <ActorBadge actor={parseActor(task.created_by_actor)} />
                  {task.due_at ? <time dateTime={task.due_at}>Due {formatDateTime(task.due_at)}</time> : null}
                </p>
              </div>
            </div>
          </li>
        ))}
        {query.data?.next_cursor != null ? <MoreExist noun="tasks" /> : null}
      </ul>
      {!query.isLoading && !query.isError && open.length === 0 ? (
        <MutedEmpty text="No open tasks. Add the next concrete action." />
      ) : null}
    </Section>
  )
}

function TaskComposer({ targetType, targetId }: { targetType: CrmTargetType; targetId: string }) {
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
          target_type: targetType,
          target_id: targetId,
        },
      }).unwrap()
      form.reset()
    } catch (error) {
      form.setError('root', { message: recordErrorMessage(error, 'The task could not be saved. Try again.') })
    }
  })
  const titleId = `task-title-${targetId}`
  const dueId = `task-due-${targetId}`

  return (
    <form onSubmit={(event) => void submit(event)} className="grid gap-2">
      <Label htmlFor={titleId}>Next action</Label>
      <Input id={titleId} placeholder="Book a discovery call" aria-invalid={Boolean(form.formState.errors.title)} {...form.register('title')} />
      {form.formState.errors.title ? <p className="text-xs text-danger">{form.formState.errors.title.message}</p> : null}
      <Label htmlFor={dueId}>Due date <span className="text-muted-foreground">(optional)</span></Label>
      <Input id={dueId} type="datetime-local" {...form.register('dueAt')} />
      <input type="hidden" {...form.register('body')} />
      {form.formState.errors.root ? <p role="alert" className="text-xs text-danger">{form.formState.errors.root.message}</p> : null}
      <Button type="submit" variant="primary" size="sm" className="justify-self-start" disabled={state.isLoading}>
        {state.isLoading ? <Loader2 className="animate-spin" aria-hidden="true" /> : <CalendarClock aria-hidden="true" />}
        Add task
      </Button>
    </form>
  )
}
