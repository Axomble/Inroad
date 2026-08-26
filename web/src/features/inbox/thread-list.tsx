import { ChevronDown, Mail, MailOpen } from 'lucide-react'
import { ReplyClassPill } from '@/components/shared/reply-class-pill'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import type { ListKeyboardNav } from '@/hooks/use-list-keyboard-nav'
import type { InboxThreadSummary } from './api'
import type { BucketGroup, ThreadBucket } from './thread-buckets'
import { contactLabel } from './contact-label'
import { SenderAvatar } from './sender-avatar'
import { LabelChips } from './label-chip'

/**
 * The message list, presented the way a desktop mail client presents it:
 * collapsible time groups ("Today ▾", …), and per thread an initials avatar,
 * the sender bold while unread, the subject in the accent ink while unread, a
 * 3px accent bar down the row's left edge for unread, and quick actions that
 * surface over the timestamp on hover.
 *
 * Grouping and collapse live in the PAGE, not here: the keyboard cursor must
 * skip the rows a collapsed group hides, and only the owner of the nav knows
 * the visible order. This component just renders what it is given.
 *
 * `nav`'s indices are over the flat VISIBLE list; each row carries its own
 * flat index via `visibleIndexById` regardless of which group it sits in.
 */
export function ThreadList({
  groups,
  collapsed,
  onToggleGroup,
  visibleIndexById,
  mailboxLabel,
  nav,
  onOpen,
  onToggleRead,
  selectedThreadId,
}: {
  groups: readonly BucketGroup<InboxThreadSummary>[]
  collapsed: ReadonlySet<ThreadBucket>
  onToggleGroup: (bucket: ThreadBucket) => void
  /** Flat index in the VISIBLE (expanded) order per thread id — the keyboard order. */
  visibleIndexById: ReadonlyMap<string, number>
  /** A mailbox's display label for its id, so a row still says which mailbox
   * it came from even in the "All mail" scope. */
  mailboxLabel: (mailboxId: string) => string
  nav: ListKeyboardNav
  onOpen: (thread: InboxThreadSummary) => void
  onToggleRead: (thread: InboxThreadSummary) => void
  /** The thread open in the reader pane, highlighted as the current one. */
  selectedThreadId?: string
}) {
  return (
    <div>
      {groups.map((group) => {
        const isCollapsed = collapsed.has(group.bucket)
        return (
          <section key={group.bucket}>
            <h3 className="sticky top-0 z-10 border-b border-border bg-surface/95 backdrop-blur">
              <button
                type="button"
                aria-expanded={!isCollapsed}
                onClick={() => onToggleGroup(group.bucket)}
                className="flex w-full items-center gap-1.5 px-3 py-1.5 text-left text-[11.5px] font-semibold text-foreground transition-colors hover:bg-surface-2/60 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
              >
                <ChevronDown
                  className={cn('size-3 shrink-0 text-muted-foreground transition-transform', isCollapsed && '-rotate-90')}
                  aria-hidden="true"
                />
                {group.label}
                {isCollapsed && (
                  <span className="font-mono text-[10px] font-normal tabular-nums text-faint">{group.items.length}</span>
                )}
              </button>
            </h3>
            {!isCollapsed && (
              <ul>
                {group.items.map((thread) => {
                  // `visibleIndexById` covers every thread in an expanded group
                  // by construction. -1 rather than 0 for the impossible miss:
                  // `nav.isActive(-1)` simply never matches, whereas 0 would
                  // make two rows share an index — both highlighting together,
                  // and Enter opening the wrong one.
                  const index = visibleIndexById.get(thread.id) ?? -1
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
                      onToggleRead={onToggleRead}
                    />
                  )
                })}
              </ul>
            )}
          </section>
        )
      })}
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
  onToggleRead,
}: {
  thread: InboxThreadSummary
  index: number
  active: boolean
  selected: boolean
  mailboxLabel: string
  onHover: (index: number) => void
  onOpen: (thread: InboxThreadSummary) => void
  onToggleRead: (thread: InboxThreadSummary) => void
}) {
  const sender = contactLabel(thread)
  return (
    <li
      data-row-index={index}
      aria-current={selected ? 'true' : undefined}
      className={cn(
        'group relative flex cursor-pointer items-start gap-2.5 border-b border-border py-2.5 pr-3 pl-4 transition-colors',
        // Three states, deliberately distinct: the thread OPEN in the reader
        // keeps a persistent tinted background, while the keyboard/hover
        // cursor is a lighter transient one. Conflating them would leave the
        // operator unable to tell which thread they are reading from which
        // they are pointing at.
        selected && 'bg-accent',
        !selected && (active ? 'bg-surface-2/60' : 'hover:bg-surface-2/40'),
      )}
      onMouseEnter={() => onHover(index)}
      onClick={() => onOpen(thread)}
    >
      {/* The unread bar down the row's left edge — a mail client's strongest
          unread signal. Never the ONLY one: the bold sender weight and the
          sr-only cue below carry the same state for a screen reader and a
          colorblind reader alike. */}
      {thread.unread && (
        <span className="absolute top-1.5 bottom-1.5 left-0 w-[3px] rounded-r-full bg-primary" aria-hidden="true" />
      )}

      <SenderAvatar label={sender} className="mt-0.5" />

      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span
            className={cn(
              'min-w-0 flex-1 truncate text-[13.5px] text-foreground',
              thread.unread ? 'font-semibold' : 'font-normal',
            )}
          >
            {thread.unread && <span className="sr-only">Unread: </span>}
            {sender}
          </span>
          {/* The timestamp yields its spot to the quick actions on hover —
              the actions stay in the DOM (focusable, discoverable by tab)
              and only their visibility is exchanged. */}
          <time
            className={cn(
              'shrink-0 font-mono text-[11px] tabular-nums transition-opacity group-focus-within:opacity-0 group-hover:opacity-0',
              thread.unread ? 'font-semibold text-accent-ink' : 'text-muted-foreground',
            )}
            dateTime={thread.last_message_at}
          >
            {relativeTime(thread.last_message_at)}
          </time>
        </div>

        <div
          className={cn(
            'truncate text-[12.5px]',
            thread.unread ? 'font-medium text-accent-ink' : 'text-muted-foreground',
          )}
        >
          {thread.subject || '(no subject)'}
        </div>

        <div className="mt-0.5 flex min-w-0 items-center gap-1.5">
          <ReplyClassPill replyClass={thread.last_reply_class} replyLabel={thread.reply_label} className="shrink-0" />
          {/* One chip, the way a mail client shows one category on a row —
              the rest surface as "+N" and in full inside the reader. */}
          <LabelChips labels={thread.labels} max={1} className="shrink-0" />
          <span className="ml-auto min-w-0 truncate font-mono text-[10px] text-faint">{mailboxLabel}</span>
        </div>
      </div>

      {/* Hover quick actions, over where the timestamp sits. */}
      <div className="absolute top-1.5 right-2 flex items-center gap-0.5 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100">
        <button
          type="button"
          aria-label={thread.unread ? `Mark ${sender} as read` : `Mark ${sender} as unread`}
          onClick={(e) => {
            // The row's own click opens the thread; this button must not.
            e.stopPropagation()
            onToggleRead(thread)
          }}
          className="flex size-6 items-center justify-center rounded-md border border-border bg-surface text-muted-foreground transition-colors hover:bg-surface-2 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        >
          {thread.unread ? <MailOpen className="size-3.5" aria-hidden="true" /> : <Mail className="size-3.5" aria-hidden="true" />}
        </button>
      </div>
    </li>
  )
}
