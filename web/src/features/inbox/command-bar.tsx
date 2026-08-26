import { PenLine, Mail, MailOpen, MailCheck } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { InboxThreadSummary } from './api'

/**
 * The command bar a desktop mail client runs across the top of its panes —
 * the primary "New mail" followed by the actions that operate on the current
 * selection and the current view. Only verbs the API actually has appear
 * here: read/unread, mark-all-read, compose. (No delete/archive/move — those
 * operations don't exist server-side, and a dead button is worse than none.)
 *
 * Selection-scoped buttons render disabled rather than vanishing when nothing
 * is selected, so the bar doesn't reflow with every click and the operator
 * learns where each verb lives.
 */
export function CommandBar({
  onCompose,
  selected,
  onToggleRead,
  unreadOnPage,
  markAllBusy,
  onMarkAllRead,
}: {
  onCompose: () => void
  /** The thread selected in the reader pane, if any — the target of the selection verbs. */
  selected: InboxThreadSummary | undefined
  onToggleRead: (thread: InboxThreadSummary) => void
  /** How many of the currently listed threads are unread — what "Mark all as read" would touch. */
  unreadOnPage: number
  markAllBusy: boolean
  onMarkAllRead: () => void
}) {
  return (
    <div className="flex h-11 shrink-0 items-center gap-1 border-b border-border bg-surface px-2 sm:px-3">
      <Button variant="primary" size="sm" onClick={onCompose}>
        <PenLine />
        New mail
      </Button>

      <span className="mx-1.5 h-5 w-px bg-border" aria-hidden="true" />

      <Button
        variant="ghost"
        size="sm"
        disabled={!selected}
        aria-label={selected?.unread ? 'Mark as read' : 'Mark as unread'}
        onClick={() => selected && onToggleRead(selected)}
      >
        {selected?.unread ? <MailOpen /> : <Mail />}
        <span className="hidden md:inline">{selected?.unread ? 'Mark as read' : 'Mark as unread'}</span>
      </Button>

      <Button
        variant="ghost"
        size="sm"
        disabled={unreadOnPage === 0 || markAllBusy}
        aria-label="Mark all as read"
        onClick={onMarkAllRead}
      >
        <MailCheck />
        <span className="hidden md:inline">Mark all as read</span>
      </Button>
    </div>
  )
}
