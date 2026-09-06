import { useState } from 'react'
import { zodResolver } from '@hookform/resolvers/zod'
import { useForm } from 'react-hook-form'
import { z } from 'zod'
import { CalendarClock, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useCrmCreateTaskMutation, type CrmTargetFields, type CrmTargetType } from './api'
import { recordErrorMessage } from './error-copy'
import { InlineLoading, MoreExist, MutedEmpty, QueryErrorBanner, Section } from '@/components/shared/record-page'
import { isOpenTask, useOpenTasks } from './use-open-tasks'
import { TaskRow } from './task-row'

const taskSchema = z.object({
  title: z.string().trim().min(1, 'Add a task title.').max(200),
  body: z.string().trim().max(20_000),
  dueAt: z.string(),
})
type TaskValues = z.infer<typeof taskSchema>

/** The open follow-up work on any CRM record, and the form that adds to it. */
export function TasksPanel({ targetType, targetId }: { targetType: CrmTargetType; targetId: string }) {
  // The shared hook, so this list and the stat strip that counts open tasks above
  // it are one query arg — one request, and a count that cannot contradict the
  // list. The rows below come from the same cached page.
  const { query } = useOpenTasks(targetType, targetId)

  /**
   * The tasks completed during this visit, which keep their place in the list —
   * struck through, labelled Completed, with Reopen beside them.
   *
   * A done task is not open work, so it is filtered out of the counts and would
   * otherwise leave the list the instant it was ticked: no confirmation that the
   * right row was ticked, and no way back from a mis-click, because nothing on
   * this panel can reach a done task again. Holding only the ones completed here
   * keeps "What's next" a list of next actions rather than an archive — the
   * durable record of the completion is the activity feed — and a reload clears
   * it, by which point undo is no longer what the operator is reaching for.
   */
  const [completedHere, setCompletedHere] = useState<readonly string[]>([])
  function trackCompletedHere(taskId: string, completed: boolean) {
    setCompletedHere((ids) =>
      completed ? (ids.includes(taskId) ? ids : [...ids, taskId]) : ids.filter((id) => id !== taskId),
    )
  }

  // The target lives on the panel, never on the task: `CrmTask` carries no
  // target_type/target_id, and `CrmTaskInput` requires both.
  const target: CrmTargetFields = { target_type: targetType, target_id: targetId }
  const shown = (query.data?.items ?? []).filter((task) => isOpenTask(task) || completedHere.includes(task.id))

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
        {shown.map((task) => (
          <TaskRow key={task.id} task={task} target={target} onCompletedHere={trackCompletedHere} />
        ))}
        {query.data?.next_cursor != null ? <MoreExist noun="tasks" /> : null}
      </ul>
      {!query.isLoading && !query.isError && shown.length === 0 ? (
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
