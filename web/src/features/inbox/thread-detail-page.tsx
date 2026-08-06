import { useEffect, useRef } from 'react'
import { Link, getRouteApi } from '@tanstack/react-router'
import { ArrowLeft } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ReplyClassPill } from '@/components/shared/reply-class-pill'
import { NotFound } from '@/components/shared/not-found'
import { EmptyBlock, Page, PageBody, PageTopbar } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import { useGetInboxThreadQuery, useSetInboxThreadReadMutation, type InboxMessage } from './api'
import { contactLabel } from './contact-label'
import { MessageBody } from './message-body'

const routeApi = getRouteApi('/app/inbox/$threadId')

/**
 * One thread, at its own address — the full back-and-forth with a contact.
 * `messages` arrives oldest-first with inbound/outbound already interleaved
 * by `occurred_at` (see the `getInboxThread` response doc), so this renders
 * it straight through, no client-side re-sort.
 */
export function ThreadDetailPage() {
  const { threadId } = routeApi.useParams()
  const { data, isLoading, error, refetch } = useGetInboxThreadQuery({ id: threadId })
  const [setRead] = useSetInboxThreadReadMutation()

  // Gmail-style: opening a thread marks it read. Guarded by a ref keyed on
  // `threadId` — not on `data`'s identity, which changes on every background
  // refetch of the SAME thread — so the mutation fires at most once per
  // thread opened, never again on a poll that merely refreshes this screen.
  const markedThreadIdRef = useRef<string | null>(null)
  useEffect(() => {
    if (!data || markedThreadIdRef.current === threadId) return
    markedThreadIdRef.current = threadId
    if (data.unread) void setRead({ id: threadId, setInboxThreadReadRequest: { unread: false } })
  }, [threadId, data, setRead])

  // A deleted or cross-tenant thread is a 404, not a generic failure — it
  // gets the app's one shared not-found screen, not a blank body or a retry
  // button that can never succeed.
  if (httpStatus(error) === 404) return <NotFound />

  return (
    <Page>
      <PageTopbar
        eyebrow="Thread"
        back={
          <Button variant="ghost" size="icon-sm" asChild className="shrink-0">
            <Link to="/app/inbox" aria-label="Back to inbox">
              <ArrowLeft className="size-4" />
            </Link>
          </Button>
        }
        title={isLoading ? <Skeleton className="h-5 w-48" /> : data && contactLabel(data)}
        subtitle={data ? data.subject || '(no subject)' : undefined}
        actions={data ? <ReplyClassPill replyClass={data.last_reply_class} /> : undefined}
      />

      <PageBody className="mx-auto w-full max-w-3xl space-y-4 px-4 py-6 sm:px-6">
        {isLoading ? (
          <div className="space-y-3">
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-24 w-full" />
          </div>
        ) : error ? (
          <EmptyBlock
            title="Couldn't load this thread"
            description={`The message history couldn't be loaded${httpStatus(error) ? ` (${httpStatus(error)})` : ''} — try again.`}
            action={
              <Button variant="secondary" size="sm" onClick={() => void refetch()}>
                Try again
              </Button>
            }
          />
        ) : (
          data?.messages.map((message, index) => (
            // `message_id` is blank for most outbound sends (no provider
            // Message-ID recorded, or none generated yet) — never a safe key
            // on its own, since two outbound messages in the same thread can
            // legitimately share the empty string. `occurred_at` isn't unique
            // either (same-second sends), so pair it with the array index —
            // safe here because `messages` is a fixed, server-sorted list
            // that this component never reorders or filters client-side.
            // oxlint-disable-next-line no-array-index-key -- fixed, server-sorted list; index+occurred_at is stable, message_id/occurred_at alone are not unique for outbound legs
            <MessageBubble key={`${message.occurred_at}-${index}`} message={message} />
          ))
        )}
      </PageBody>
    </Page>
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
