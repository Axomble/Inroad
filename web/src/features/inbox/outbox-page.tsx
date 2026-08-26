import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Undo2, AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Page, PageTopbar, PageBody, EmptyBlock, ListHeader, ListHeaderCell } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { cn } from '@/lib/utils'
import {
  useListInboxOutboxQuery,
  useCancelInboxPendingReplyMutation,
  type InboxPendingReply,
} from './api'
import { sendTimingLabel, PENDING_STATUS_LABELS, type PendingStatus } from './undo-countdown'
import { inboxErrorMessage } from './inbox-search'

/**
 * The outbox: every manual reply queued but not yet delivered, across the
 * workspace.
 *
 * A separate page rather than a panel in the inbox, because the question it
 * answers is workspace-wide ("what is about to go out?") rather than
 * thread-local — the reader's own UndoSendPill already covers the thread case.
 */
export function OutboxPage() {
  // `now` is captured once per render rather than per row, so every row's timing
  // is measured against one instant. No interval: this page lists scheduled
  // sends, most of them hours or days out, and the thread's own pill is where a
  // live countdown belongs.
  const [now] = useState(() => new Date())
  const { data, isLoading, error, refetch } = useListInboxOutboxQuery(
    {},
    // Queued replies drain on their own as the worker delivers them, and no
    // client mutation marks that — so a remount re-reads rather than showing a
    // list that quietly went stale.
    { refetchOnMountOrArgChange: true },
  )

  const items = data?.items ?? []

  return (
    <Page>
      <PageTopbar eyebrow="Outbox" subtitle="Replies waiting to go out, and the ones you can still stop" />

      <PageBody>
        {isLoading ? (
          <LoadingRows />
        ) : error !== undefined ? (
          <EmptyBlock
            title="Couldn't load the outbox"
            description={inboxErrorMessage(error)}
            action={
              <Button variant="secondary" size="sm" onClick={() => void refetch()}>
                Try again
              </Button>
            }
          />
        ) : items.length === 0 ? (
          <EmptyBlock
            title="Nothing waiting"
            description="Replies you send appear here briefly, and scheduled ones stay until they go out."
          />
        ) : (
          <>
            <ListHeader>
              <ListHeaderCell className="min-w-0 flex-1">Reply</ListHeaderCell>
              <ListHeaderCell className="w-40">Sending</ListHeaderCell>
              <ListHeaderCell className="w-24 text-right">Action</ListHeaderCell>
            </ListHeader>
            <ul>
              {items.map((item) => (
                <OutboxRow key={item.id} item={item} now={now} />
              ))}
            </ul>
          </>
        )}
      </PageBody>
    </Page>
  )
}

function OutboxRow({ item, now }: { item: InboxPendingReply; now: Date }) {
  const [cancel, { isLoading, error }] = useCancelInboxPendingReplyMutation()
  const status = item.status as PendingStatus

  return (
    <li className="flex items-center gap-4 border-b border-border px-5 py-3">
      <div className="min-w-0 flex-1">
        <Link
          to="/app/inbox/$threadId"
          params={{ threadId: item.thread_id }}
          className="block truncate text-[13.5px] font-medium text-foreground underline-offset-2 hover:text-accent-ink hover:underline"
        >
          {item.thread_subject || '(no subject)'}
        </Link>
        <p className="truncate text-[12px] text-muted-foreground">
          {item.contact_email || 'No linked contact'}
          {' · '}
          <span className="font-mono text-[11px]">{item.body_text.slice(0, 80)}</span>
        </p>
        {/* A failed reply keeps its row precisely so this can be shown — the
            alternative is the reply silently vanishing. */}
        {item.last_error && (
          <p className="mt-0.5 flex items-start gap-1 text-[11px] text-warn">
            <AlertCircle className="mt-px size-3 shrink-0" aria-hidden="true" />
            <span className="truncate">{item.last_error}</span>
          </p>
        )}
      </div>

      <div className="w-40 shrink-0">
        <p className="text-[12px] text-foreground">{sendTimingLabel(item.send_after, now)}</p>
        <p
          className={cn(
            'font-mono text-[10px] uppercase',
            status === 'failed' ? 'text-danger' : 'text-faint',
          )}
        >
          {PENDING_STATUS_LABELS[status] ?? status}
        </p>
      </div>

      <div className="flex w-24 shrink-0 flex-col items-end gap-1">
        {item.cancellable ? (
          <Button
            variant="outline"
            size="xs"
            disabled={isLoading}
            onClick={() => void cancel({ pendingId: item.id })}
          >
            <Undo2 className="size-3" />
            Cancel
          </Button>
        ) : (
          <span className="text-[11px] text-faint">On its way</span>
        )}
        {error !== undefined && (
          <p role="alert" className="text-right text-[10px] text-warn">
            {httpStatus(error) === 409
              ? 'Already sent.'
              : `Couldn't cancel${httpStatus(error) ? ` (${httpStatus(error)})` : ''}.`}
          </p>
        )}
      </div>
    </li>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1, 2].map((i) => (
        <li key={i} className="flex items-center gap-4 border-b border-border px-5 py-3.5">
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-56" />
            <Skeleton className="h-2.5 w-72" />
          </div>
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-6 w-20" />
        </li>
      ))}
    </ul>
  )
}
