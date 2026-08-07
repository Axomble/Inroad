import { ReplyClassPill } from '@/components/shared/reply-class-pill'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import type { ListKeyboardNav } from '@/hooks/use-list-keyboard-nav'
import type { InboxThreadSummary } from './api'
import { contactLabel } from './contact-label'

/**
 * Dense thread rows: unread dot, contact (primary), subject (secondary),
 * reply-class pill, relative time + mailbox (secondary, beside the
 * timestamp — still worth showing, just not the headline).
 */
export function ThreadList({
  threads,
  mailboxLabel,
  nav,
  onOpen,
}: {
  threads: readonly InboxThreadSummary[]
  /** A mailbox's display label for its id, so a row still says which mailbox
   * it came from even in the "All mailboxes" scope. */
  mailboxLabel: (mailboxId: string) => string
  nav: ListKeyboardNav
  onOpen: (thread: InboxThreadSummary) => void
}) {
  return (
    <ul>
      {threads.map((thread, index) => (
        <ThreadRow
          key={thread.id}
          thread={thread}
          index={index}
          active={nav.isActive(index)}
          mailboxLabel={mailboxLabel(thread.mailbox_id)}
          onHover={nav.onRowHover}
          onOpen={onOpen}
        />
      ))}
    </ul>
  )
}

function ThreadRow({
  thread,
  index,
  active,
  mailboxLabel,
  onHover,
  onOpen,
}: {
  thread: InboxThreadSummary
  index: number
  active: boolean
  mailboxLabel: string
  onHover: (index: number) => void
  onOpen: (thread: InboxThreadSummary) => void
}) {
  return (
    <li
      data-row-index={index}
      className={cn(
        'flex cursor-pointer items-center gap-3 border-b border-border px-5 py-3 transition-colors',
        // The keyboard cursor and hover share one highlight, so "current"
        // always means the same thing however you got there.
        active ? 'bg-surface-2/60' : 'hover:bg-surface-2/40',
      )}
      onMouseEnter={() => onHover(index)}
      onClick={() => onOpen(thread)}
    >
      {/* A dot alone would make "unread" a color-only signal; the bold
          contact-name weight and the sr-only label carry the same state for a
          screen reader and a colorblind reader alike. */}
      <span
        className={cn('size-1.5 shrink-0 rounded-full', thread.unread ? 'bg-primary' : 'bg-transparent')}
        aria-hidden="true"
      />
      <div className="min-w-0 flex-1">
        <div
          className={cn(
            'truncate text-[13.5px]',
            thread.unread ? 'font-semibold text-foreground' : 'font-medium text-muted-foreground',
          )}
        >
          {thread.unread && <span className="sr-only">Unread: </span>}
          {contactLabel(thread)}
        </div>
        <div className="truncate text-[12px] text-muted-foreground">{thread.subject || '(no subject)'}</div>
      </div>
      <ReplyClassPill replyClass={thread.last_reply_class} replyLabel={thread.reply_label} className="shrink-0" />
      <div className="flex w-28 shrink-0 flex-col items-end gap-0.5">
        <time className="font-mono text-[11px] text-muted-foreground" dateTime={thread.last_message_at}>
          {relativeTime(thread.last_message_at)}
        </time>
        <span className="truncate font-mono text-[10px] text-faint">{mailboxLabel}</span>
      </div>
    </li>
  )
}
