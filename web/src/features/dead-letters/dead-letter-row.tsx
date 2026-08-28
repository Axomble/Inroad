import { useState } from 'react'
import { AlertCircle, ChevronDown, Loader2 } from 'lucide-react'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import type { TaskDeadLetter } from '@/store/api'
import { useDiscardTaskDeadLetterMutation, useReplayTaskDeadLetterMutation } from './api'
import {
  attemptsText,
  deadLetterActionMessage,
  DISCARD_CONFIRM,
  lastErrorText,
  payloadText,
  REPLAY_CONFIRM,
  statusCopy,
} from './dead-letter-copy'

type Action = 'replay' | 'discard'

/**
 * One task the system gave up on.
 *
 * The row leads with `task_type` rather than a friendly name because the contract
 * calls the vocabulary open — new handlers add new types — so there is no closed
 * set to translate from, and inventing labels for the ones we know would make the
 * unknown ones look like a rendering bug. It is shown in mono for the same reason
 * an id is: it is a value to be matched against logs, not prose.
 *
 * Actions live on the row and not in a detail view: the decision an operator makes
 * here is per-task, and requiring a navigation to reach it turns triaging twenty
 * rows into forty page loads.
 */
export function DeadLetterRow({ letter }: { letter: TaskDeadLetter }) {
  const [confirming, setConfirming] = useState<Action | null>(null)
  const [showPayload, setShowPayload] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)

  const [replay, { isLoading: replaying }] = useReplayTaskDeadLetterMutation()
  const [discard, { isLoading: discarding }] = useDiscardTaskDeadLetterMutation()
  const busy = replaying || discarding

  const status = statusCopy(letter.status)

  async function run(action: Action) {
    setActionError(null)
    const mutate = action === 'replay' ? replay : discard
    try {
      await mutate({ id: letter.id }).unwrap()
      setConfirming(null)
    } catch (error) {
      // Kept on the row rather than raised to a toast: which task failed is half
      // the message, and a 409 in particular is about THIS row's state.
      setActionError(deadLetterActionMessage(error, action))
      setConfirming(null)
    }
  }

  return (
    <li data-slot="dead-letter" className="border-b border-border px-4 py-3 sm:px-5">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="min-w-0">
          <p className="flex flex-wrap items-center gap-2">
            <span data-slot="dead-letter-type" className="truncate font-mono text-[12.5px] text-foreground">
              {letter.task_type}
            </span>
            <span
              data-slot="dead-letter-status"
              title={status.detail}
              className={cn(
                'shrink-0 rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase tracking-[0.12em]',
                status.actionable ? 'bg-warm/15 text-warm' : 'bg-surface-2 text-faint',
              )}
            >
              {status.label}
            </span>
          </p>
          <p data-slot="dead-letter-meta" className="mt-0.5 text-[11px] leading-snug text-muted-foreground">
            {attemptsText(letter.attempt_count)} · gave up {relativeTime(letter.created_at)}
            {letter.replayed_at && ` · replayed ${relativeTime(letter.replayed_at)}`}
          </p>
        </div>

        {/*
          Only an untriaged row offers actions. A replayed or discarded row is
          terminal by the contract, and a disabled button there would suggest the
          state is a permission problem rather than a finished decision.
        */}
        {status.actionable && (
          <div className="flex shrink-0 gap-1.5">
            <Button variant="outline" size="sm" disabled={busy} onClick={() => setConfirming('replay')}>
              {replaying && <Loader2 className="size-3.5 animate-spin" />}
              Replay
            </Button>
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => setConfirming('discard')}>
              Discard
            </Button>
          </div>
        )}
      </div>

      <p data-slot="dead-letter-error" className="mt-1.5 max-w-prose text-[11px] leading-snug text-faint">
        {lastErrorText(letter.last_error)}
      </p>

      <button
        type="button"
        onClick={() => setShowPayload((open) => !open)}
        aria-expanded={showPayload}
        className="mt-1.5 flex items-center gap-1 font-mono text-[10px] uppercase tracking-[0.12em] text-faint hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
      >
        <ChevronDown className={cn('size-3 transition-transform', showPayload && 'rotate-180')} aria-hidden="true" />
        Payload
      </button>
      {showPayload && (
        <pre
          data-slot="dead-letter-payload"
          className="mt-1.5 max-h-64 overflow-auto rounded border border-border bg-surface/60 p-2 text-[11px] leading-snug text-muted-foreground"
        >
          {payloadText(letter.payload)}
        </pre>
      )}

      {actionError && (
        <p
          role="alert"
          data-slot="dead-letter-action-error"
          className="mt-2 flex items-start gap-1.5 text-[11px] leading-snug text-danger"
        >
          <AlertCircle className="mt-px size-3.5 shrink-0" aria-hidden="true" />
          <span>{actionError}</span>
        </p>
      )}

      <AlertDialog open={confirming !== null} onOpenChange={(next) => !next && setConfirming(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirming === 'replay' ? 'Replay this task?' : 'Discard this task?'}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirming === 'replay' ? REPLAY_CONFIRM : DISCARD_CONFIRM}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button variant="ghost" size="sm" onClick={() => setConfirming(null)} disabled={busy}>
              Cancel
            </Button>
            {/*
              Replay is the destructive-looking one because it is the one with an
              effect outside this screen: it can put mail on the wire. Discard only
              closes a row.
            */}
            <Button
              variant={confirming === 'replay' ? 'destructive' : 'outline'}
              size="sm"
              disabled={busy}
              onClick={() => void run(confirming === 'replay' ? 'replay' : 'discard')}
            >
              {busy && <Loader2 className="size-3.5 animate-spin" />}
              {confirming === 'replay' ? 'Replay task' : 'Discard task'}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </li>
  )
}
