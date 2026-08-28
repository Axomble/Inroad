import { useState } from 'react'
import { AlertCircle } from 'lucide-react'
import { EmptyBlock, Page, PageBody, PageTopbar } from '@/components/layout/page'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { popCursor, pushCursor, type CursorStack } from '@/lib/cursor-stack'
import { cn } from '@/lib/utils'
import { useListTaskDeadLettersQuery } from './api'
import { DeadLetterRow } from './dead-letter-row'
import {
  deadLetterErrorMessage,
  EMPTY_ALL,
  emptyFiltered,
  PAGE_INTRO,
  STATUS_COPY,
  type DeadLetterStatus,
  type StatusFilter,
} from './dead-letter-copy'

// How many rows to ask for. A page size the client chooses, NOT a copy of a server
// policy constant: the server's cap (200) and default are its own, and nothing here
// re-derives either. `next_cursor` — not this number — decides whether more exist.
const PAGE_SIZE = 50

const FILTERS: readonly StatusFilter[] = ['all', 'pending', 'replayed', 'discarded']

function filterLabel(filter: StatusFilter): string {
  return filter === 'all' ? 'All' : STATUS_COPY[filter].label
}

/**
 * Background work that permanently failed.
 *
 * This exists because the API has served it since the endpoint shipped and nothing
 * rendered it: a send whose provider kept rejecting it was dropped silently, and the
 * only way to know was to read the database. An operator cannot act on mail they
 * cannot see did not go out.
 *
 * Deliberately not on the overview or the pulse card. An empty queue is the normal
 * state and would be a permanent zero on a dashboard, which is how a number stops
 * being read — and by the time it is non-zero it needs the payload and the actions
 * that only fit on a screen of their own.
 */
export function DeadLettersPage() {
  const [filter, setFilter] = useState<StatusFilter>('pending')
  const [cursor, setCursor] = useState<string | undefined>(undefined)
  const [visited, setVisited] = useState<CursorStack>([])

  const { data, isLoading, isFetching, isError, error } = useListTaskDeadLettersQuery({
    // The contract's filter is "omit for all of them", so 'all' sends nothing rather
    // than a sentinel string the server would reject with a 422.
    ...(filter === 'all' ? {} : { status: filter }),
    limit: PAGE_SIZE,
    ...(cursor === undefined ? {} : { cursor }),
  })

  const letters = data?.items ?? []
  // The server answers this now, and the answer is exact. The previous version grew
  // `limit` per page and inferred "more exist" from a full page, which was wrong
  // twice over: the service silently clamps limit at 200, so the fifth page asked
  // for 250, got 200, compared 200 to 250, and hid the button — leaving every row
  // past the cap unreachable with no error. `next_cursor` is absent on the last page
  // and present otherwise; a short page is not the end-of-list signal.
  const nextCursor = data?.next_cursor
  const canGoBack = visited.length > 0

  function goNext() {
    if (nextCursor === undefined) return
    setVisited((stack) => pushCursor(stack, cursor))
    setCursor(nextCursor)
  }

  function goBack() {
    const { stack, cursor: previous } = popCursor(visited)
    setVisited(stack)
    // No prev_cursor on this response to fall back on — a forward-only triage list —
    // so an exhausted stack goes home rather than nowhere.
    setCursor(previous)
  }

  function changeFilter(next: StatusFilter) {
    setFilter(next)
    // Both, and this is load-bearing: a cursor is only valid under the filter that
    // minted it, and the server now answers a carried-over one with a 400. Dropping
    // the cursor without clearing the stack would leave Previous holding cursors
    // from a list the operator is no longer looking at.
    setCursor(undefined)
    setVisited([])
  }

  return (
    <Page>
      <PageTopbar eyebrow="Failed tasks" />

      {isError && (
        <div role="alert" className="flex items-start gap-2 border-b border-danger/30 bg-danger/10 px-5 py-2.5 text-xs text-danger">
          <AlertCircle className="mt-px size-4 shrink-0" aria-hidden="true" />
          <span>{deadLetterErrorMessage(error, "Couldn't load failed tasks. Refresh the page to try again.")}</span>
        </div>
      )}

      <PageBody>
        <p className="max-w-prose px-4 pt-3 text-[11.5px] leading-snug text-muted-foreground sm:px-5">{PAGE_INTRO}</p>

        <div role="group" aria-label="Filter by state" className="flex flex-wrap gap-1.5 px-4 py-3 sm:px-5">
          {FILTERS.map((option) => (
            <button
              key={option}
              type="button"
              onClick={() => changeFilter(option)}
              aria-pressed={filter === option}
              className={cn(
                'rounded-lg px-2.5 py-1 text-[12px] transition-colors',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
                filter === option
                  ? 'bg-surface-2 font-medium text-foreground shadow-[inset_0_0_0_1px_var(--border)]'
                  : 'text-muted-foreground hover:bg-surface-2 hover:text-foreground',
              )}
            >
              {filterLabel(option)}
            </button>
          ))}
        </div>

        {isLoading ? (
          <LoadingRows />
        ) : isError ? (
          // Nothing below the banner: an empty list under "couldn't load" would read
          // as "no task has failed", which is the one thing this screen must never
          // say when it does not know.
          null
        ) : letters.length === 0 ? (
          <EmptyBlock
            title={filter === 'all' ? 'Nothing has been dropped' : `No ${filterLabel(filter).toLowerCase()} tasks`}
            description={filter === 'all' ? EMPTY_ALL : emptyFiltered(filter as DeadLetterStatus)}
          />
        ) : (
          <>
            <ul>
              {letters.map((letter) => (
                <DeadLetterRow key={letter.id} letter={letter} />
              ))}
            </ul>
            {(canGoBack || nextCursor !== undefined) && (
              <div className="flex items-center gap-2 px-4 py-3 sm:px-5">
                <Button variant="outline" size="sm" disabled={!canGoBack || isFetching} onClick={goBack}>
                  Previous
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={nextCursor === undefined || isFetching}
                  onClick={goNext}
                >
                  Next
                </Button>
              </div>
            )}
          </>
        )}
      </PageBody>
    </Page>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1, 2].map((i) => (
        <li key={i} className="border-b border-border px-4 py-3 sm:px-5">
          <div className="space-y-2">
            <Skeleton className="h-3.5 w-48" />
            <Skeleton className="h-2.5 w-64" />
          </div>
        </li>
      ))}
    </ul>
  )
}
