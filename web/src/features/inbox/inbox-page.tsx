import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ListSearchInput } from '@/components/shared/list-search-input'
import { SortMenu } from '@/components/shared/sort-menu'
import { Page, PageTopbar, SectionBar, PageBody, EmptyBlock, ListHeader, ListHeaderCell, HintBar } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { useUrlPatch } from '@/hooks/use-url-state'
import { useDebouncedInput } from '@/hooks/use-debounced-input'
import { useListKeyboardNav, LIST_NAV_HINTS } from '@/hooks/use-list-keyboard-nav'
import { useListMailboxesQuery } from '@/store/api'
import { useListInboxThreadsQuery, type InboxThreadSummary } from './api'
import { ThreadList } from './thread-list'
import {
  parseInboxSearch,
  encodeCursor,
  decodeCursor,
  pushCursor,
  popCursor,
  isStaleCursorError,
  inboxErrorMessage,
  type CursorStack,
} from './inbox-search'

/** The reply classes the backend can assign, per components/shared/reply-class-pill.tsx. */
const REPLY_CLASS_FILTERS = [
  { id: '', label: 'All replies' },
  { id: 'positive', label: 'Positive' },
  { id: 'negative', label: 'Negative' },
  { id: 'neutral', label: 'Neutral' },
  { id: 'out_of_office', label: 'Out of office' },
  { id: 'auto_reply', label: 'Auto-reply' },
  { id: 'unsubscribe', label: 'Unsubscribed' },
  { id: 'unknown', label: 'Unknown' },
] as const

const PAGE_SIZE = 25

/**
 * Cap for the rail's own unfiltered fetch — the API's own maximum (200), so
 * the per-mailbox counts reflect as much of the workspace as one request can
 * without a dedicated counts endpoint. Real numbers from a real response, not
 * invented — just not a guaranteed exhaustive total on a very large inbox.
 */
const RAIL_SAMPLE_SIZE = 200

export function InboxPage() {
  const search = parseInboxSearch(useSearch({ strict: false }))
  const patch = useUrlPatch()
  const navigate = useNavigate()
  const selectedMailbox = search.mailbox ?? ''
  const replyClass = search.class ?? ''

  const { data: mailboxes, error: mailboxesError } = useListMailboxesQuery()
  const mailboxesById = useMemo(() => new Map((mailboxes ?? []).map((m) => [m.id ?? '', m])), [mailboxes])
  const mailboxLabel = (id: string) => mailboxesById.get(id)?.email || id

  // Unfiltered by mailbox/class on purpose: the scope rail's job is "how many
  // threads live in each mailbox", a question the currently-selected
  // reply-class filter shouldn't change.
  const { data: railData } = useListInboxThreadsQuery({ limit: RAIL_SAMPLE_SIZE }, { refetchOnMountOrArgChange: true })
  // `railData?.items ?? []` would hand `useMemo` a fresh array literal on every
  // render for as long as `railData` is undefined — memoize the fallback too,
  // not just the derived Map, so the dependency is actually stable.
  const allThreads = useMemo(() => railData?.items ?? [], [railData])
  const totalCount = allThreads.length
  const countByMailbox = useMemo(() => {
    const counts = new Map<string, number>()
    for (const t of allThreads) counts.set(t.mailbox_id, (counts.get(t.mailbox_id) ?? 0) + 1)
    return counts
  }, [allThreads])

  // Keyset pagination can name the next page but not "page N back" (and the
  // response carries no prev_cursor to fall back on either — unlike
  // contacts), so the pages already visited are stacked as they're left. Not
  // URL state: it's this tab's history, and a pasted link legitimately
  // arrives without one.
  const [stack, setStack] = useState<CursorStack>([])
  const [recoveredFromStaleCursor, setRecoveredFromStaleCursor] = useState(false)

  useEffect(() => {
    if (!search.cursor) setStack([])
  }, [search.cursor])

  const decodedCursor = decodeCursor(search.cursor)
  const {
    data: page,
    currentData,
    isLoading,
    isFetching,
    error,
    refetch,
  } = useListInboxThreadsQuery(
    {
      mailboxId: selectedMailbox || undefined,
      replyClass: replyClass || undefined,
      q: search.q,
      beforeLastMessageAt: decodedCursor?.beforeLastMessageAt,
      beforeId: decodedCursor?.beforeId,
      limit: PAGE_SIZE,
    },
    // Replies land from a background IMAP poll, not a client mutation, so no
    // cache tag can invalidate a stale page on its own — same reasoning as
    // the contacts list's identical setting.
    { refetchOnMountOrArgChange: true },
  )
  const busy = isFetching && !currentData

  const staleCursor = search.cursor !== undefined && isStaleCursorError(error)
  useEffect(() => {
    if (!staleCursor) return
    setStack([])
    setRecoveredFromStaleCursor(true)
    patch({ cursor: undefined })
  }, [staleCursor, patch])

  // Any control except the pager changes what is being listed, which makes
  // the current cursor meaningless — it points into the old result set.
  const selectMailbox = (id: string) => {
    setRecoveredFromStaleCursor(false)
    setStack([])
    patch({ mailbox: id || undefined, cursor: undefined })
  }
  const selectClass = (id: string) => {
    setRecoveredFromStaleCursor(false)
    setStack([])
    patch({ class: id || undefined, cursor: undefined })
  }
  const applyQuery = (next: string | undefined) => {
    setRecoveredFromStaleCursor(false)
    setStack([])
    patch({ q: next, cursor: undefined })
  }
  // Echoes every keystroke immediately but only commits (writes the URL, hits
  // the server) once the user pauses — a real per-keystroke request would be
  // wasteful and would spam the history stack via useUrlPatch's replace.
  const [typedQuery, setTypedQuery] = useDebouncedInput(search.q ?? '', (next) => applyQuery(next.trim() || undefined))

  const items = page?.items ?? []

  // The full-vs-partial page is the one honest signal for "is there more":
  // a page that came back short of the limit cannot have another page after
  // it, and a full page might. Not a guess — a fact about what just loaded.
  const hasNextPage = items.length > 0 && items.length === PAGE_SIZE
  const canGoPrev = stack.length > 0 || search.cursor !== undefined

  const goNext = () => {
    const last = items[items.length - 1]
    if (!last || !hasNextPage) return
    setRecoveredFromStaleCursor(false)
    setStack((s) => pushCursor(s, search.cursor))
    patch({ cursor: encodeCursor(last.last_message_at, last.id) })
  }
  const goPrev = () => {
    setRecoveredFromStaleCursor(false)
    if (stack.length > 0) {
      const { stack: rest, cursor } = popCursor(stack)
      setStack(rest)
      patch({ cursor })
    } else {
      // No prev_cursor on this response to fall back to (unlike contacts): a
      // deep link with no walked history can only recover to the first page.
      patch({ cursor: undefined })
    }
  }

  // Opening a thread is navigation, not a mark-read trigger — the reader at
  // `/app/inbox/$threadId` marks it read on mount (Gmail-style), which keeps
  // exactly one place responsible for that side effect.
  const openThread = (thread: InboxThreadSummary) => {
    void navigate({ to: '/app/inbox/$threadId', params: { threadId: thread.id } })
  }

  const nav = useListKeyboardNav({
    count: items.length,
    onOpen: (index) => {
      const thread = items[index]
      if (thread) openThread(thread)
    },
  })

  const showError = error !== undefined && !staleCursor
  const hasActiveFilter = Boolean(selectedMailbox || replyClass || search.q)
  const isEmptyInbox = !isLoading && !showError && items.length === 0 && !hasActiveFilter && !search.cursor
  const isEmptyFiltered = !isLoading && !showError && items.length === 0 && !isEmptyInbox

  return (
    <Page>
      <PageTopbar eyebrow="Inbox" subtitle="Read + triage replies across every connected mailbox" />

      <PageBody className="flex flex-col overflow-hidden md:flex-row">
        <div className="flex max-h-40 w-full shrink-0 flex-col overflow-y-auto border-b border-border md:max-h-none md:w-56 md:border-b-0 md:border-r">
          <ScopeButton label="All mailboxes" count={totalCount} active={selectedMailbox === ''} onSelect={() => selectMailbox('')} />
          {mailboxesError ? (
            <p role="alert" className="p-3 text-[11px] text-danger">
              Couldn't load mailboxes{httpStatus(mailboxesError) ? ` (${httpStatus(mailboxesError)})` : ''}.
            </p>
          ) : (
            <ul className="grid grid-cols-2 sm:grid-cols-3 md:block">
              {(mailboxes ?? []).map((m) => (
                <li key={m.id}>
                  <ScopeButton
                    label={m.email ?? 'Mailbox'}
                    count={countByMailbox.get(m.id ?? '') ?? 0}
                    active={selectedMailbox === m.id}
                    onSelect={() => selectMailbox(m.id ?? '')}
                  />
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="flex min-w-0 flex-1 flex-col">
          <SectionBar label={selectedMailbox ? mailboxLabel(selectedMailbox) : 'All mailboxes'}>
            <ListSearchInput value={typedQuery} onChange={setTypedQuery} placeholder="Search subject or contact email…" />
            <SortMenu options={REPLY_CLASS_FILTERS} value={replyClass} onChange={selectClass} />
          </SectionBar>

          {recoveredFromStaleCursor && (
            <p role="status" className="border-b border-border px-5 py-1.5 text-xs text-warn">
              That page link has expired — showing the first page.
            </p>
          )}

          {isLoading ? (
            <PageBody>
              <LoadingRows />
            </PageBody>
          ) : showError ? (
            <PageBody>
              <EmptyBlock
                title="Couldn't load the inbox"
                description={inboxErrorMessage(error)}
                action={
                  <Button variant="secondary" size="sm" onClick={() => void refetch()}>
                    Try again
                  </Button>
                }
              />
            </PageBody>
          ) : isEmptyInbox ? (
            <PageBody>
              <EmptyBlock
                title="No replies yet"
                description="Matched replies from your campaigns land here as they arrive, classified by tone."
              />
            </PageBody>
          ) : isEmptyFiltered ? (
            <PageBody>
              <EmptyBlock
                title="No threads match"
                description="Nothing matches the selected mailbox, reply-class filter, and search."
                action={
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => {
                      selectMailbox('')
                      selectClass('')
                      applyQuery(undefined)
                    }}
                  >
                    Clear filters
                  </Button>
                }
              />
            </PageBody>
          ) : (
            <>
              <ListHeader>
                <ListHeaderCell className="min-w-0 flex-1">Thread</ListHeaderCell>
                <ListHeaderCell className="w-16 text-right">Updated</ListHeaderCell>
              </ListHeader>

              <div
                ref={nav.containerRef}
                aria-busy={busy}
                className={cn('flex-1 overflow-y-auto transition-opacity', busy && 'opacity-50')}
              >
                <ThreadList threads={items} mailboxLabel={mailboxLabel} nav={nav} onOpen={openThread} />
              </div>

              <div className="flex items-center gap-2 border-t border-border px-4 py-2 sm:px-5">
                <span className="font-mono text-[11px] tabular-nums text-faint">
                  {items.length === 1 ? '1 thread' : `${items.length} threads`}
                </span>
                {busy && <span className="text-[11px] text-muted-foreground">Loading…</span>}
                <div className="ml-auto flex items-center gap-2">
                  <Button variant="outline" size="sm" aria-label="Previous page" disabled={!canGoPrev || busy} onClick={goPrev}>
                    Previous
                  </Button>
                  <Button variant="outline" size="sm" aria-label="Next page" disabled={!hasNextPage || busy} onClick={goNext}>
                    Next
                  </Button>
                </div>
              </div>

              <HintBar hints={LIST_NAV_HINTS} />
            </>
          )}
        </div>
      </PageBody>
    </Page>
  )
}

function ScopeButton({
  label,
  count,
  active,
  onSelect,
}: {
  label: string
  count: number
  active: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={active ? 'true' : undefined}
      className={cn(
        'flex w-full items-center gap-2 px-4 py-2 text-left text-[13px] text-muted-foreground transition-colors',
        'hover:bg-surface-2 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
        active && 'bg-surface-2 font-medium text-foreground',
      )}
    >
      <span className="min-w-0 flex-1 truncate">{label}</span>
      <span className="shrink-0 rounded-md bg-surface-2 px-1.5 py-0.5 font-mono text-[10px] tabular-nums text-muted-foreground">
        {count}
      </span>
    </button>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1, 2].map((i) => (
        <li key={i} className="flex items-center gap-4 border-b border-border px-5 py-3.5">
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-48" />
            <Skeleton className="h-2.5 w-64" />
          </div>
          <Skeleton className="h-4 w-16" />
        </li>
      ))}
    </ul>
  )
}
