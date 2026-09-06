import { useState } from 'react'
import { CalendarClock, Check, Loader2, RotateCcw, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { StatusPill } from '@/components/shared/status-pill'
import { formatDateTime } from '@/lib/datetime'
import { cn } from '@/lib/utils'
import {
  useCrmDeleteTaskMutation,
  useCrmUpdateTaskMutation,
  type CrmTargetFields,
  type CrmTask,
  type CrmTaskInput,
} from './api'
import { recordErrorMessage } from './error-copy'
import { parseActor } from './actor'
import { ActorBadge } from './actor-badge'

/**
 * The body for `PUT /crm/tasks/{id}`, which is a FULL REPLACE: the API stores
 * exactly what it is sent, so a status-only payload does not merely change the
 * status — it blanks the title and body and drops the due date and assignee.
 * Every field is therefore resent from the task being acted on.
 *
 * `target` is a separate argument because the task does not carry one: `CrmTask`
 * (the GET shape) has no target_type/target_id — the list endpoint is already
 * scoped to a single record — while `CrmTaskInput` requires both. They come from
 * the panel's props, the only place they exist. Don't go looking on `task`.
 */
function fullTaskInput(task: CrmTask, status: CrmTask['status'], target: CrmTargetFields): CrmTaskInput {
  return {
    title: task.title,
    body: task.body,
    due_at: task.due_at,
    assignee_user_id: task.assignee_user_id,
    status,
    ...target,
  }
}

/**
 * One task, and the three things that can happen to it: complete it, put it back,
 * or delete it.
 *
 * Failures are reported on the row rather than at the top of the page, because
 * *which* task failed is half the message — a record can carry a dozen of them.
 */
export function TaskRow({
  task,
  target,
  onCompletedHere,
}: {
  task: CrmTask
  target: CrmTargetFields
  /** Tells the panel to keep this row on screen after it is completed. */
  onCompletedHere: (taskId: string, completed: boolean) => void
}) {
  const [updateTask, { isLoading: saving }] = useCrmUpdateTaskMutation()
  const [deleteTask, { isLoading: deleting }] = useCrmDeleteTaskMutation()
  const [problem, setProblem] = useState<string | null>(null)
  const [confirmingDelete, setConfirmingDelete] = useState(false)

  const done = task.status === 'done'
  const busy = saving || deleting

  async function setStatus(status: CrmTask['status']) {
    const result = await updateTask({ id: task.id, crmTaskInput: fullTaskInput(task, status, target) })
    if ('error' in result) {
      setProblem(
        recordErrorMessage(
          result.error,
          status === 'done' ? 'The task could not be completed. Try again.' : 'The task could not be reopened. Try again.',
        ),
      )
      return
    }
    setProblem(null)
    onCompletedHere(task.id, status === 'done')
  }

  async function confirmDelete() {
    const result = await deleteTask({ id: task.id })
    // Close first: a failure rendered under an open dialog is a failure nobody reads.
    setConfirmingDelete(false)
    if ('error' in result) {
      setProblem(recordErrorMessage(result.error, 'The task could not be deleted. Try again.'))
    }
  }

  return (
    <li className="rounded-lg border border-border bg-background p-3">
      <div className="flex items-start gap-2">
        {done ? (
          <Check className="mt-0.5 size-4 text-ok" aria-hidden="true" />
        ) : (
          <CalendarClock className="mt-0.5 size-4 text-accent-ink" aria-hidden="true" />
        )}
        <div className="min-w-0 flex-1">
          <p className={cn('text-sm font-medium', done && 'text-muted-foreground line-through')}>{task.title}</p>
          <p className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            {/* A word, not just a struck-through line: state never rides on styling alone. */}
            {done ? <StatusPill tone="done">Completed</StatusPill> : null}
            <ActorBadge actor={parseActor(task.created_by_actor)} />
            {task.due_at ? <time dateTime={task.due_at}>Due {formatDateTime(task.due_at)}</time> : null}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {done ? (
            <Button
              variant="ghost"
              size="sm"
              disabled={busy}
              aria-label={`Reopen ${task.title}`}
              onClick={() => void setStatus('open')}
            >
              {saving ? <Loader2 className="animate-spin" aria-hidden="true" /> : <RotateCcw aria-hidden="true" />}
              Reopen
            </Button>
          ) : (
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              aria-label={`Mark ${task.title} as complete`}
              onClick={() => void setStatus('done')}
            >
              {saving ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Check aria-hidden="true" />}
              Complete
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon-sm"
            disabled={busy}
            aria-label={`Delete ${task.title}`}
            onClick={() => setConfirmingDelete(true)}
          >
            {deleting ? (
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
            ) : (
              <Trash2 className="size-3.5" aria-hidden="true" />
            )}
          </Button>
        </div>
      </div>
      {problem ? <p role="alert" className="mt-2 text-xs text-danger">{problem}</p> : null}

      <AlertDialog open={confirmingDelete} onOpenChange={(next) => !next && setConfirmingDelete(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete “{task.title}”?</AlertDialogTitle>
            <AlertDialogDescription>
              The task disappears from this record for everyone, and that cannot be undone — the API keeps no
              copy to restore. If the work actually happened, mark it complete instead: completing records that
              it was done, deleting says it should never have been here.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setConfirmingDelete(false)} disabled={deleting}>
              Cancel
            </Button>
            <Button variant="destructive" size="sm" disabled={deleting} onClick={() => void confirmDelete()}>
              {deleting && <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />}
              Delete task
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </li>
  )
}
