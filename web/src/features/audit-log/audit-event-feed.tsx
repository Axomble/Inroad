import { useState } from 'react'
import { Loader2 } from 'lucide-react'
import { EmptyBlock } from '@/components/layout/page'
import { QueryErrorBanner } from '@/components/shared/record-page'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useListAuditEventsQuery, type ListAuditEventsApiArg } from '@/store/api'
import { AuditEventHeader, AuditEventRow } from './audit-event-row'
import { auditLogErrorMessage, END_OF_LOG } from './audit-log-copy'
import type { AuditFilterArgs } from './audit-log-search'

// A page size the client chooses, not a copy of the server's default or its cap
// of 200. `next_cursor` — not a full page — is what says more exist.
const PAGE_SIZE = 50

function pageArgs(filters: AuditFilterArgs, cursor: string | undefined): ListAuditEventsApiArg {
  return { ...filters, limit: PAGE_SIZE, ...(cursor === undefined ? {} : { cursor }) }
}

/**
 * The log for one filter set, grown a page at a time by "Load older events".
 *
 * Each loaded page is its own subscription to the generated query, keyed by the
 * cursor that reached it, so the pages already on screen are never refetched or
 * reshuffled when another is appended. The cursors are this component's state and
 * nothing else's: the caller keys it by the filter set, so a filter change
 * remounts it and the old cursors — which the server binds to the filters that
 * minted them and would refuse under new ones — are dropped with it.
 */
export function AuditEventFeed({
  filters,
  empty,
}: {
  filters: AuditFilterArgs
  /** What to say when the first page is empty — the caller knows whether filters caused it. */
  empty: { title: string; description: string; action?: React.ReactNode }
}) {
  const [cursors, setCursors] = useState<readonly string[]>([])
  const lastCursor = cursors.at(-1)

  // The same arguments as the last page's own subscription, so RTK dedupes it into
  // one request. Read here because the footer — the button, the retry, the end
  // marker — belongs to the feed: it stays mounted while a page loads, so keyboard
  // focus on "Load older events" survives the click.
  const last = useListAuditEventsQuery(pageArgs(filters, lastCursor))
  const lastPage = last.currentData
  const firstLoad = cursors.length === 0 && lastPage === undefined
  const loadingMore = cursors.length > 0 && lastPage === undefined && !last.isError
  const nextCursor = lastPage?.next_cursor ?? undefined
  const hasRows = cursors.length > 0 || (lastPage?.events.length ?? 0) > 0

  function loadMore() {
    if (nextCursor === undefined) return
    setCursors((current) => [...current, nextCursor])
  }

  return (
    <div data-slot="audit-feed">
      {hasRows && <AuditEventHeader />}
      <AuditEventPage filters={filters} cursor={undefined} />
      {cursors.map((cursor) => (
        <AuditEventPage key={cursor} filters={filters} cursor={cursor} />
      ))}

      {last.isError ? (
        // A failed first page gets the banner and nothing else: an empty list under
        // "couldn't load" would read as "nothing happened", which is the one thing an
        // audit log must never claim when it does not know.
        <QueryErrorBanner
          message={auditLogErrorMessage(last.error)}
          onRetry={() => void last.refetch()}
          retrying={last.isFetching}
          className="mx-4 my-3 sm:mx-5"
        />
      ) : firstLoad ? (
        <LoadingRows />
      ) : !hasRows ? (
        <EmptyBlock title={empty.title} description={empty.description} action={empty.action} />
      ) : nextCursor !== undefined || loadingMore ? (
        <div className="flex justify-center px-4 py-3 sm:px-5">
          <Button variant="outline" size="sm" onClick={loadMore} disabled={loadingMore || last.isFetching}>
            {loadingMore && <Loader2 className="animate-spin" aria-hidden="true" />}
            {loadingMore ? 'Loading older events' : 'Load older events'}
          </Button>
        </div>
      ) : (
        <p role="status" className="px-4 py-3 text-center text-[12px] text-faint sm:px-5">
          {END_OF_LOG}
        </p>
      )}
    </div>
  )
}

/**
 * One page's rows. Renders nothing until its data arrives — the feed's footer
 * owns loading and failure, so a page never draws a second spinner or banner.
 */
function AuditEventPage({ filters, cursor }: { filters: AuditFilterArgs; cursor: string | undefined }) {
  const { currentData } = useListAuditEventsQuery(pageArgs(filters, cursor), {
    // Only the first page. An audit log is read to find out what just happened, so
    // reopening it must not replay a cached minute-old answer; a later page is a
    // keyset slice behind a fixed cursor and cannot change.
    refetchOnMountOrArgChange: cursor === undefined,
  })
  if (currentData === undefined || currentData.events.length === 0) return null

  return (
    <ul>
      {currentData.events.map((event) => (
        <AuditEventRow key={event.id} event={event} />
      ))}
    </ul>
  )
}

function LoadingRows() {
  return (
    <ul aria-label="Loading audit events">
      {[0, 1, 2, 3].map((i) => (
        <li key={i} className="flex gap-4 border-b border-border px-4 py-3 sm:px-5">
          <Skeleton className="h-3.5 w-24" />
          <Skeleton className="h-3.5 w-32" />
          <Skeleton className="h-3.5 w-48" />
        </li>
      ))}
    </ul>
  )
}
