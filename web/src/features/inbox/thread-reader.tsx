import { useEffect, useRef } from 'react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ReplyClassPill } from '@/components/shared/reply-class-pill'
import { EmptyBlock } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import { useGetInboxThreadQuery, useSetInboxThreadReadMutation, type InboxMessage } from './api'
import { contactLabel } from './contact-label'
import { MessageBody } from './message-body'
import { ReplyComposer } from './reply-composer'
import { SnoozeMenu } from './snooze-menu'
import { LabelPicker } from './label-picker'
import { LabelChips } from './label-chip'

/**
 * One thread's messages and its composer — the reader, with no page chrome of
 * its own.
 *
 * Extracted from thread-detail-page.tsx so the same reader serves two hosts
 * without duplicating the fetch, the mark-read effect, or the message
 * rendering: the three-pane inbox mounts it in its right pane, and
 * `/app/inbox/$threadId` mounts it as a full page (which is still a real
 * address — a deep link, and the only layout below `lg`).
 *
 * `header` is rendered by the host rather than here, because the two hosts
 * need genuinely different chrome: the standalone page needs a back button and
 * a PageTopbar, the pane needs neither.
 */
export function ThreadReader({ threadId, className }: { threadId: string; className?: string }) {
  const { data, isLoading, error, refetch } = useGetInboxThreadQuery({ id: threadId })
  const [setRead] = useSetInboxThreadReadMutation()

  // Gmail-style: opening a thread marks it read. Guarded by a ref keyed on
  // `threadId` — not on `data`'s identity, which changes on every background
  // refetch of the SAME thread — so the mutation fires at most once per
  // thread opened, never again on a poll that merely refreshes this screen.
  //
  // In the three-pane layout this component stays mounted while `threadId`
  // changes, which the ref handles correctly: a new id no longer matches, so
  // the next thread is marked read exactly once too.
  const markedThreadIdRef = useRef<string | null>(null)
  useEffect(() => {
    if (!data || markedThreadIdRef.current === threadId) return
    markedThreadIdRef.current = threadId
    if (data.unread) void setRead({ id: threadId, setInboxThreadReadRequest: { unread: false } })
  }, [threadId, data, setRead])

  if (isLoading) {
    return (
      <div className={cn('space-y-3', className)}>
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    )
  }

  if (error) {
    return (
      <div className={className}>
        <EmptyBlock
          title="Couldn't load this thread"
          description={`The message history couldn't be loaded${httpStatus(error) ? ` (${httpStatus(error)})` : ''} — try again.`}
          action={
            <Button variant="secondary" size="sm" onClick={() => void refetch()}>
              Try again
            </Button>
          }
        />
      </div>
    )
  }

  if (!data) return null

  return (
    <div className={cn('space-y-4', className)}>
      {data.messages.map((message, index) => (
        // `message_id` is blank for most outbound sends (no provider
        // Message-ID recorded, or none generated yet) — never a safe key on its
        // own, since two outbound messages in the same thread can legitimately
        // share the empty string. `occurred_at` isn't unique either (same-second
        // sends), so pair it with the array index — safe here because
        // `messages` is a fixed, server-sorted list that this component never
        // reorders or filters client-side.
        // oxlint-disable-next-line no-array-index-key -- fixed, server-sorted list; index+occurred_at is stable, message_id/occurred_at alone are not unique for outbound legs
        <MessageBubble key={`${message.occurred_at}-${index}`} message={message} />
      ))}
      <ReplyComposer threadId={threadId} hasInboundMessage={data.messages.some((m) => m.direction === 'inbound')} />
    </div>
  )
}

/**
 * The reader's own heading — who the thread is with, its subject, and its
 * reply class. Exported so the pane can render it inline while the standalone
 * page feeds the same facts into a PageTopbar.
 */
export function ThreadReaderHeading({ threadId }: { threadId: string }) {
  const { data } = useGetInboxThreadQuery({ id: threadId })
  if (!data) return null
  return (
    <div className="flex min-w-0 items-start justify-between gap-3">
      <div className="min-w-0">
        <h2 className="truncate text-sm font-semibold text-foreground">{contactLabel(data)}</h2>
        <p className="truncate text-[12px] text-muted-foreground">{data.subject || '(no subject)'}</p>
      </div>
      <div className="flex shrink-0 items-start gap-2">
        <LabelChips labels={data.labels} max={2} />
        <ReplyClassPill replyClass={data.last_reply_class} replyLabel={data.reply_label} />
        <LabelPicker threadId={threadId} applied={data.labels} />
        <SnoozeMenu threadId={threadId} snooze={data.snooze} />
      </div>
    </div>
  )
}

/** A single message in the thread, its own bubble — sender/time header + the sanitized body. */
function MessageBubble({ message }: { message: InboxMessage }) {
  const outbound = message.direction === 'outbound'
  // "You" for outbound is a text label, not a color: an inbound bubble's
  // sender name/email carries the same weight of information the other way.
  const sender = outbound ? 'You' : message.from_name || message.from_email || 'Unknown sender'

  return (
    <article
      className={cn(
        'max-w-[85%] rounded-xl border border-border p-4',
        outbound ? 'ml-auto bg-primary/5' : 'bg-surface-2/40',
      )}
    >
      <header className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
        <span className="text-sm font-semibold text-foreground">{sender}</span>
        <time className="font-mono text-[11px] text-muted-foreground" dateTime={message.occurred_at}>
          {relativeTime(message.occurred_at)}
        </time>
      </header>
      <MessageBody bodyText={message.body_text} bodyHtml={message.body_html} />
    </article>
  )
}
