import { useMemo } from 'react'
import { ReplyClassPill } from '@/components/shared/reply-class-pill'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import type { ListKeyboardNav } from '@/hooks/use-list-keyboard-nav'
import type { InboxThreadSummary } from './api'
import { contactLabel } from './contact-label'
import { groupByBucket } from './thread-buckets'

/**
 * Dense thread rows grouped into time buckets ("Today", "Yesterday", …), the
 * way a mail client presents a list: unread dot, contact (primary), subject
 * (secondary), reply-class pill, relative time + mailbox.
 *
 * `nav`'s indices are over the FLAT list, not per group, so keyboard
 * navigation runs straight down the visible order regardless of where the
 * group boundaries fall. Each row therefore carries its own flat index rather
 * than its position within its bucket.
 */
export function ThreadList({
  threads,
  mailboxLabel,
  nav,
  onOpen,
  selectedThreadId,
}: {
  threads: readonly InboxThreadSummary[]
  /** A mailbox's display label for its id, so a row still says which mailbox
   * it came from even in the "All mailboxes" scope. */
  mailboxLabel: (mailboxId: string) => string
  nav: ListKeyboardNav
  onOpen: (thread: InboxThreadSummary) => void
  /** The thread open in the reader pane, highlighted as the current one. */
  selectedThreadId?: string
}) {
  // `now` is captured once per threads-identity rather than per row, so every
  // row in one render buckets against the same instant — see bucketFor's doc.
  const groups = useMemo(() => groupByBucket(threads, (t) => t.last_message_at, new Date()), [threads])

  // The flat index each thread occupies, so a row inside a group still knows
  // its position in the keyboard order.
  const indexById = useMemo(() => new Map(threads.map((t, i) => [t.id, i])), [threads])

  return (
    <div>
      {groups.map((group) => (
        <section key={group.bucket}>
          <h3 className="sticky top-0 z-10 border-b border-border bg-surface/95 px-5 py-1.5 font-mono text-[10px] tracking-wide text-faint uppercase backdrop-blur">
            {group.label}
          </h3>
          <ul>
            {group.items.map((thread) => {
              // `indexById` is built from the very array being rendered, so the
              // lookup cannot miss. -1 rather than 0 for the impossible case:
              // `nav.isActive(-1)` simply never matches, whereas 0 would make
              // two rows share an index — both highlighting together, and Enter
              // opening the wrong one.
              const index = indexById.get(thread.id) ?? -1
              return (
                <ThreadRow
                  key={thread.id}
                  thread={thread}
                  index={index}
                  active={nav.isActive(index)}
                  selected={thread.id === selectedThreadId}
                  mailboxLabel={mailboxLabel(thread.mailbox_id)}
                  onHover={nav.onRowHover}
                  onOpen={onOpen}
                />
              )
            })}
          </ul>
        </section>
      ))}
    </div>
  )
}

function ThreadRow({
  thread,
  index,
  active,
  selected,
  mailboxLabel,
  onHover,
  onOpen,
}: {
  thread: InboxThreadSummary
  index: number
  active: boolean
  selected: boolean
  mailboxLabel: string
  onHover: (index: number) => void
  onOpen: (thread: InboxThreadSummary) => void
}) {
  return (
    <li
      data-row-index={index}
      aria-current={selected ? 'true' : undefined}
      className={cn(
        'flex cursor-pointer items-center gap-3 border-b border-border px-5 py-3 transition-colors',
        // Three states, deliberately distinct: the thread OPEN in the reader
        // keeps a persistent left marker, while the keyboard/hover cursor is
        // a transient background. Conflating them would leave the operator
        // unable to tell which thread they are reading from which they are
        // pointing at.
        selected && 'border-l-2 border-l-primary bg-surface-2/70 pl-[18px]',
        !selected && (active ? 'bg-surface-2/60' : 'hover:bg-surface-2/40'),
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
