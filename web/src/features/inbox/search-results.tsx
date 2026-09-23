import { Button } from '@/components/ui/button'
import { EmptyBlock, HintBar, PageBody } from '@/components/layout/page'
import { HighlightedText } from '@/components/shared/highlighted-text'
import { LIST_NAV_HINTS, type ListKeyboardNav } from '@/hooks/use-list-keyboard-nav'
import { cn } from '@/lib/utils'
import type { InboxSearchHit, InboxThreadSummary } from './api'
import { ThreadRow } from './thread-list'
import { matchedLegsLabel, searchErrorCopy, SEARCH_QUERY_MAX_LENGTH } from './inbox-search'

/**
 * The list pane while a search is active: the hits, newest first (the API's
 * order — not relevance), each drawn as the same row the inbox list uses so a
 * result looks and behaves like the thread it opens, plus the matched
 * snippet and which side of the conversation matched.
 *
 * Pure presentation. The page owns the query, because the selection, the
 * keyboard cursor and the command bar's verbs must all see the same threads
 * whether the pane is listing or searching.
 */
export function SearchResults({
  query,
  scopeLabel,
  hits,
  pending,
  busy,
  error,
  queryTooLong,
  hasNextPage,
  isFetchingNextPage,
  onLoadMore,
  onRetry,
  onClearSearch,
  containerRef,
  nav,
  mailboxLabel,
  onOpen,
  onToggleRead,
  selectedThreadId,
}: {
  /** The committed (trimmed) query, for the empty state's copy. */
  query: string
  /** The folder/mailbox/category the search runs inside — its filters apply. */
  scopeLabel: string
  hits: readonly InboxSearchHit[]
  /** Nothing to show yet: the first page of this search is still loading. */
  pending: boolean
  /** A new search is loading over the previous one's (dimmed) results. */
  busy: boolean
  /** The query's error, if the last request failed. */
  error: unknown
  /** The query is over the server's length cap, so it was never sent. */
  queryTooLong: boolean
  hasNextPage: boolean
  isFetchingNextPage: boolean
  onLoadMore: () => void
  /** Re-runs whatever failed: the next page when that was it, else the search. */
  onRetry: () => void
  onClearSearch: () => void
  /** The scroll container the keyboard cursor scrolls within — owned by the page's nav. */
  containerRef: ListKeyboardNav['containerRef']
  // Only the row-facing half of the nav, as ThreadList takes it.
  nav: Pick<ListKeyboardNav, 'isActive' | 'onRowHover'>
  mailboxLabel: (mailboxId: string) => string
  onOpen: (thread: InboxThreadSummary) => void
  onToggleRead: (thread: InboxThreadSummary) => void
  selectedThreadId?: string
}) {
  const clearAction = (
    <Button variant="secondary" size="sm" onClick={onClearSearch}>
      Clear search
    </Button>
  )

  if (queryTooLong) {
    return (
      <PageBody>
        <EmptyBlock
          title="That search is too long"
          description={`Searches are limited to ${SEARCH_QUERY_MAX_LENGTH} characters — shorten it to search.`}
          action={clearAction}
        />
      </PageBody>
    )
  }

  if (pending) {
    return (
      <PageBody>
        <p role="status" className="px-5 py-6 text-center text-[12.5px] text-muted-foreground">
          Searching…
        </p>
      </PageBody>
    )
  }

  // With results already on screen a failure (a next page, or a refetch after
  // an invalidation) is reported beside them rather than replacing them —
  // what loaded is still true.
  const failed = error !== undefined
  if (failed && hits.length === 0) {
    const copy = searchErrorCopy(error)
    return (
      <PageBody>
        <EmptyBlock
          title={copy.title}
          description={copy.description}
          action={
            copy.retryable ? (
              <Button variant="secondary" size="sm" onClick={onRetry}>
                Try again
              </Button>
            ) : (
              clearAction
            )
          }
        />
      </PageBody>
    )
  }

  if (hits.length === 0) {
    return (
      <PageBody>
        <EmptyBlock
          title={`No threads match “${query}”`}
          description={`Nothing in ${scopeLabel} mentions it in a subject or message, on either side of the conversation. Try fewer or different words.`}
          action={clearAction}
        />
      </PageBody>
    )
  }

  return (
    <>
      <div
        ref={containerRef}
        aria-busy={busy}
        className={cn('flex-1 overflow-y-auto transition-opacity', busy && 'opacity-50')}
      >
        <ul aria-label={`Search results for ${query}`}>
          {hits.map((hit, index) => (
            <ThreadRow
              key={hit.thread.id}
              thread={hit.thread}
              index={index}
              active={nav.isActive(index)}
              selected={hit.thread.id === selectedThreadId}
              mailboxLabel={mailboxLabel(hit.thread.mailbox_id)}
              onHover={nav.onRowHover}
              onOpen={onOpen}
              onToggleRead={onToggleRead}
              subject={matchedSubject(hit)}
            >
              <HitDetails hit={hit} />
            </ThreadRow>
          ))}
        </ul>
      </div>

      {failed && (
        <p role="alert" className="flex items-center gap-2 border-t border-border px-4 py-1.5 text-xs text-danger sm:px-5">
          {searchErrorCopy(error).description}
          <Button variant="ghost" size="sm" className="ml-auto" onClick={onRetry}>
            Try again
          </Button>
        </p>
      )}

      <div className="flex items-center gap-2 border-t border-border px-4 py-2 sm:px-5">
        <span className="font-mono text-[12px] tabular-nums text-faint">
          {hits.length === 1 ? '1 result' : `${hits.length} results`}
          {!hasNextPage && hits.length > 1 && ' · all shown'}
        </span>
        {hasNextPage && (
          <Button variant="outline" size="sm" className="ml-auto" disabled={isFetchingNextPage} onClick={onLoadMore}>
            {isFetchingNextPage ? 'Loading…' : 'Load more'}
          </Button>
        )}
      </div>

      <HintBar hints={LIST_NAV_HINTS} />
    </>
  )
}

/**
 * The highlighted subject, when the subject is where the query matched. A
 * subject with no matched run falls back to the thread's own (the row's
 * default), so a body-only match doesn't swap the familiar subject for the
 * matching message's "Re: …" variant of it.
 */
function matchedSubject(hit: InboxSearchHit) {
  const segments = hit.snippet?.subject
  if (!segments?.some((s) => s.match)) return undefined
  return <HighlightedText segments={segments} />
}

/** The matched snippet, then which leg(s) matched — always in words. */
function HitDetails({ hit }: { hit: InboxSearchHit }) {
  const { snippet } = hit
  return (
    <>
      {snippet && snippet.body.length > 0 && (
        <p className="mt-0.5 line-clamp-2 text-[12px] break-words text-muted-foreground">
          {snippet.direction === 'outbound' && <span className="text-faint">You: </span>}
          <HighlightedText segments={snippet.body} />
        </p>
      )}
      <p className="mt-0.5 font-mono text-[11px] text-faint">Matched in {matchedLegsLabel(hit.matched_legs)}</p>
    </>
  )
}
