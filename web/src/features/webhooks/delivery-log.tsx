import { useEffect, useState } from 'react'
import { AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill } from '@/components/shared/status-pill'
import { popCursor, pushCursor, type CursorStack } from '@/lib/cursor-stack'
import { relativeTime } from '@/lib/relative-time'
import { httpStatus } from '@/lib/rtk-error'
import type { WebhookDelivery } from '@/store/api'
import { useListWebhookDeliveriesQuery } from './api'
import {
  deliveryAttemptsText,
  deliveryErrorMessage,
  DELIVERY_LOG_EMPTY,
  DELIVERY_STATUS_COPY,
  responseStatusText,
  STALE_DELIVERY_CURSOR_NOTICE,
} from './webhook-copy'

// A page size chosen for this inline log, not a copy of the server's default
// (50) or cap (100) — `next_cursor` is what decides whether more exist, never
// this number.
const PAGE_SIZE = 25

/**
 * One endpoint's delivery attempts, newest first — the keyset pager mirrors
 * `dead-letters-page.tsx` rather than inventing a second pagination pattern:
 * same `CursorStack` for Previous, same "absent/null cursor means last page"
 * reading, same 400-recovery for a cursor the server no longer honours.
 *
 * Unlike the dead-letter list, this contract's `next_cursor` is present-but-
 * `null` on the last page rather than omitted — `WebhookDeliveryList` requires
 * the field — so "more exist" is `!== null`, not `!== undefined`.
 */
export function DeliveryLog({ endpointId }: { endpointId: string }) {
  const [cursor, setCursor] = useState<string | undefined>(undefined)
  const [visited, setVisited] = useState<CursorStack>([])
  const [recovered, setRecovered] = useState(false)

  const { currentData, isLoading, isFetching, isError, error } = useListWebhookDeliveriesQuery({
    id: endpointId,
    limit: PAGE_SIZE,
    ...(cursor === undefined ? {} : { cursor }),
  })

  // currentData, not data: after Next/Previous, `data` would keep the outgoing
  // page's rows on screen under the new page's pager state.
  const deliveries = currentData?.items ?? []
  const nextCursor = currentData?.next_cursor ?? null
  const canGoBack = visited.length > 0
  const showPager = canGoBack || nextCursor !== null

  const staleCursor = cursor !== undefined && httpStatus(error) === 400
  useEffect(() => {
    if (!staleCursor) return
    setCursor(undefined)
    setVisited([])
    setRecovered(true)
  }, [staleCursor])

  // Both pagers clear the recovery notice. It reports a ONE-TIME event ("we
  // reset your position"), so leaving it up while the operator pages normally
  // turns a true statement about the last page into a false one about this
  // page — and it never came down at all, because nothing else ever set
  // `recovered` back to false.
  function goNext() {
    if (nextCursor === null) return
    setRecovered(false)
    setVisited((stack) => pushCursor(stack, cursor))
    setCursor(nextCursor)
  }

  function goBack() {
    setRecovered(false)
    const { stack, cursor: previous } = popCursor(visited)
    setVisited(stack)
    setCursor(previous)
  }

  return (
    <div data-slot="delivery-log" className="border-t border-border bg-surface/40 px-4 py-3 sm:px-5">
      {recovered && !isError && (
        <p role="status" data-slot="delivery-stale-cursor-notice" className="mb-2 text-[12px] text-muted-foreground">
          {STALE_DELIVERY_CURSOR_NOTICE}
        </p>
      )}

      {/*
        `staleCursor` renders as loading, not as an error. The 400 it stands for
        is a condition this component has ALREADY decided how to handle — the
        effect above resets to the first page and the refetch is in flight — so
        announcing it through `role="alert"` would interrupt a screen reader
        with a failure that is gone by the next frame. A skeleton is the honest
        description of what is happening.
      */}
      {isLoading || staleCursor ? (
        <LoadingRows />
      ) : isError ? (
        <p role="alert" className="flex items-start gap-1.5 text-[12px] leading-snug text-danger">
          <AlertCircle className="mt-px size-3.5 shrink-0" aria-hidden="true" />
          <span>{deliveryErrorMessage(error, "Couldn't load this endpoint's delivery log. Try again.")}</span>
        </p>
      ) : deliveries.length === 0 ? (
        <p className="text-[12px] text-muted-foreground">{DELIVERY_LOG_EMPTY}</p>
      ) : (
        <ul className="flex flex-col gap-1.5">
          {deliveries.map((delivery) => (
            <DeliveryRow key={delivery.id} delivery={delivery} />
          ))}
        </ul>
      )}

      {!isError && showPager && (
        <div className="mt-2 flex items-center gap-2">
          <Button variant="outline" size="sm" disabled={!canGoBack || isFetching} onClick={goBack}>
            Previous
          </Button>
          <Button variant="outline" size="sm" disabled={nextCursor === null || isFetching} onClick={goNext}>
            Next
          </Button>
        </div>
      )}
    </div>
  )
}

function DeliveryRow({ delivery }: { delivery: WebhookDelivery }) {
  const status = DELIVERY_STATUS_COPY[delivery.status]
  return (
    <li
      data-slot="delivery-row"
      className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-border bg-surface px-3 py-2 text-[12px]"
    >
      <StatusPill tone={status.tone}>{status.label}</StatusPill>
      <span className="font-mono text-foreground">{delivery.event_type}</span>
      <span className="text-muted-foreground">{responseStatusText(delivery.response_status)}</span>
      <span className="text-muted-foreground">{deliveryAttemptsText(delivery.attempts)}</span>
      <span className="text-faint">{relativeTime(delivery.created_at)}</span>
      {delivery.status === 'failed' && delivery.last_error && (
        <span className="w-full text-danger">{delivery.last_error}</span>
      )}
    </li>
  )
}

function LoadingRows() {
  return (
    <div className="flex flex-col gap-1.5">
      {[0, 1].map((i) => (
        <Skeleton key={i} className="h-8 w-full" />
      ))}
    </div>
  )
}
