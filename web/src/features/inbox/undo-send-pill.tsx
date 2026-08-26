import { useEffect, useState } from 'react'
import { Undo2, Send, AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { httpStatus } from '@/lib/rtk-error'
import { useCancelInboxPendingReplyMutation, type InboxPendingReply } from './api'
import { sendTimingLabel, showsCountdown } from './undo-countdown'

/**
 * The live "sending in Ns — Undo" strip for a reply queued on this thread.
 *
 * Ticks once a second only while a countdown is actually on screen: a reply
 * scheduled for next Tuesday needs no interval, and an always-on timer on a
 * long-lived inbox tab is pure waste.
 */
export function UndoSendPill({ pending }: { pending: InboxPendingReply }) {
  const [now, setNow] = useState(() => new Date())
  const [cancel, { isLoading, error }] = useCancelInboxPendingReplyMutation()

  const ticking = showsCountdown(pending.send_after, now)

  useEffect(() => {
    if (!ticking) return
    const id = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(id)
    // `ticking` is the whole dependency: once the countdown lapses the interval
    // is torn down, and it is never created for a far-future schedule.
  }, [ticking])

  // Re-read the clock when a NEW pending reply arrives, so its countdown starts
  // from the right instant rather than from whenever this component mounted.
  useEffect(() => {
    setNow(new Date())
  }, [pending.id])

  // A claimed reply is past the point of no return: the server would answer 409,
  // so the control says so rather than offering a click that cannot work.
  const canUndo = pending.cancellable

  return (
    <div className="flex flex-col gap-1 rounded-md border border-border bg-surface-2/60 px-2.5 py-1.5">
      <div className="flex min-w-0 items-center gap-2">
        <Send className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        {/* role=status, not alert: this is progress information the operator
            asked for, not a problem. aria-live is polite by default for status,
            so a screen reader is not interrupted every second. */}
        <span role="status" className="min-w-0 flex-1 truncate text-[12px] text-foreground">
          {sendTimingLabel(pending.send_after, now)}
        </span>
        {canUndo ? (
          <Button
            variant="secondary"
            size="xs"
            disabled={isLoading}
            onClick={() => void cancel({ pendingId: pending.id })}
          >
            <Undo2 className="size-3" />
            Undo
          </Button>
        ) : (
          <span className="shrink-0 text-[11px] text-faint">On its way</span>
        )}
      </div>

      {error !== undefined && (
        <p role="alert" className="flex items-start gap-1 text-[11px] text-warn">
          <AlertCircle className="mt-px size-3 shrink-0" aria-hidden="true" />
          <span>
            {/* 409 is the race this feature is defined by: the worker claimed
                the reply between the page rendering Undo and the click landing.
                Saying so is far better than a bare failure, because nothing is
                broken — the mail simply went. */}
            {httpStatus(error) === 409
              ? 'Too late — this reply is already on its way.'
              : `Couldn't undo this reply${httpStatus(error) ? ` (${httpStatus(error)})` : ''}.`}
          </span>
        </p>
      )}
    </div>
  )
}
