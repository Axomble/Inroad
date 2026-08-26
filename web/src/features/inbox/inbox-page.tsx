import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { MailOpen } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ListSearchInput } from '@/components/shared/list-search-input'
import { Page, PageTopbar, SectionBar, PageBody, EmptyBlock, HintBar } from '@/components/layout/page'
import { useUrlPatch } from '@/hooks/use-url-state'
import { useDebouncedInput } from '@/hooks/use-debounced-input'
import { useListKeyboardNav, LIST_NAV_HINTS } from '@/hooks/use-list-keyboard-nav'
import { useMediaQuery } from '@/hooks/use-media-query'
import { useListMailboxesQuery } from '@/store/api'
// Cross-feature query-hook imports are allowed for read-only reference data
// (see features/campaigns/campaign-form.tsx). Cross-feature UI imports remain
// forbidden.
import { useListReplyLabelsQuery } from '@/features/reply-labels/api'
import {
  useGetInboxOverviewQuery,
  useListInboxLabelsQuery,
  useListInboxThreadsQuery,
  useSetInboxThreadReadMutation,
  type InboxThreadSummary,
} from './api'
import { ThreadList } from './thread-list'
import { FolderPane } from './folder-pane'
import { CommandBar } from './command-bar'
import { ReplyFilterMenu } from './reply-filter-menu'
import { ThreadReader, ThreadReaderHeading } from './thread-reader'
import { ComposeWindow } from './compose-window'
import { groupByBucket, type ThreadBucket } from './thread-buckets'
import {
  parseInboxSearch,
  encodeCursor,
  decodeCursor,
  pushCursor,
  popCursor,
  isStaleCursorError,
  inboxErrorMessage,
  timezoneOffsetMinutes,
  scopeTimezoneOffset,
  SCOPE_LABELS,
  type CursorStack,
  type InboxScope,
} from './inbox-search'

const ALL_REPLIES_FILTER = { id: '', label: 'All replies' }

const PAGE_SIZE = 25

/**
 * The breakpoint at which the reader becomes a third pane instead of its own
 * route. Below it there is no room for three columns, so opening a thread
 * navigates to `/app/inbox/$threadId` as it always has.
 *
 * Kept in sync with the `lg:` variants in FolderPane and the shell below —
 * Tailwind's `lg` is 64rem, and matchMedia needs the same number in a form it
 * can parse.
 */
const THREE_PANE_QUERY = '(min-width: 64rem)'

export function InboxPage() {
  const search = parseInboxSearch(useSearch({ strict: false }))
  const patch = useUrlPatch()
  const navigate = useNavigate()
  const selectedMailbox = search.mailbox ?? ''
  const replyClass = search.class ?? ''
  const scope: InboxScope = search.scope ?? 'all'
  const selectedLabel = search.label ?? ''
  const threePane = useMediaQuery(THREE_PANE_QUERY)
  // One compose window at a time. A multi-window composer is a real feature,
  // but it needs its own stacking/focus model — and one is what the operator
  // reaches for from an inbox.
  const [composing, setComposing] = useState(false)

  const { data: mailboxes, error: mailboxesError } = useListMailboxesQuery()
  const mailboxesById = useMemo(() => new Map((mailboxes ?? []).map((m) => [m.id ?? '', m])), [mailboxes])
  const mailboxLabel = useCallback(
    (id: string) => mailboxesById.get(id)?.email || id,
    [mailboxesById],
  )
  const mailboxOptions = useMemo(
    () => (mailboxes ?? []).map((m) => ({ id: m.id ?? '', label: m.email ?? 'Mailbox' })),
    [mailboxes],
  )

  // The filter's options are the workspace's own reply-label taxonomy (each
  // filter value is the label's raw `key`, the same string stored on
  // `last_reply_class` and sent as the `class` query param) — not a hardcoded
  // set of the legacy built-in classes. While labels are still loading this
  // degrades to just "All replies" rather than blocking the page on them.
  const { data: replyLabelsData } = useListReplyLabelsQuery()
  const replyClassFilters = useMemo(
    () => [ALL_REPLIES_FILTER, ...(replyLabelsData?.labels.map((l) => ({ id: l.key, label: l.label })) ?? [])],
    [replyLabelsData],
  )

  // The folder pane's category section. Fetched unconditionally (unlike the
  // picker's own skip-until-open query, which RTK Query dedupes with this
  // one): the pane is always visible, so its labels are always needed.
  const { data: labelData } = useListInboxLabelsQuery()
  const labels = useMemo(() => labelData?.labels ?? [], [labelData])

  // Real counts for the folder pane, counted by the database over the whole
  // workspace — this replaces counting one 200-row page client-side, which was
  // honest about being a sample but wrong on any inbox larger than it.
  const { data: overview, error: overviewError } = useGetInboxOverviewQuery(
    { tzOffset: timezoneOffsetMinutes() },
    { refetchOnMountOrArgChange: true },
  )

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
      // 'all' is the API's own default; sending it explicitly would only make
      // the cache key noisier for an identical request.
      scope: scope === 'all' ? undefined : scope,
      label: selectedLabel || undefined,
      // Only the calendar-dependent scopes read it; see scopeTimezoneOffset.
      tzOffset: scopeTimezoneOffset(scope),
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
  const resetPaging = () => {
    setRecoveredFromStaleCursor(false)
    setStack([])
  }
  // The folder pane shows ONE selection, so choosing any of the three clears
  // the other two: a mailbox, a virtual folder, and a category are alternative
  // ways to say "show me this pile", not filters to intersect.
  const selectMailbox = (id: string) => {
    resetPaging()
    patch({ mailbox: id || undefined, scope: undefined, label: undefined, cursor: undefined })
  }
  const selectScope = (next: InboxScope) => {
    resetPaging()
    patch({ scope: next === 'all' ? undefined : next, mailbox: undefined, label: undefined, cursor: undefined })
  }
  const selectLabel = (id: string) => {
    resetPaging()
    patch({ label: id || undefined, mailbox: undefined, scope: undefined, cursor: undefined })
  }
  const selectClass = (id: string) => {
    resetPaging()
    patch({ class: id || undefined, cursor: undefined })
  }
  const applyQuery = (next: string | undefined) => {
    resetPaging()
    patch({ q: next, cursor: undefined })
  }
  // Echoes every keystroke immediately but only commits (writes the URL, hits
  // the server) once the user pauses — a real per-keystroke request would be
  // wasteful and would spam the history stack via useUrlPatch's replace.
  const [typedQuery, setTypedQuery] = useDebouncedInput(search.q ?? '', (next) => applyQuery(next.trim() || undefined))

  // Memoized so the fallback isn't a fresh array literal on every render while
  // `page` is undefined — otherwise every derived memo and effect downstream
  // (the bucket grouping, the selection guard) re-runs each render.
  const items = useMemo(() => page?.items ?? [], [page])

  // The time groups and their collapse state live HERE, not in ThreadList: the
  // keyboard cursor below must skip the rows a collapsed group hides, and only
  // the owner of the nav can know the visible order. Collapse is view state
  // for this visit — deliberately not persisted, and deliberately keyed by
  // bucket so it survives paging within the same view.
  const groups = useMemo(() => groupByBucket(items, (t) => t.last_message_at, new Date()), [items])
  const [collapsedBuckets, setCollapsedBuckets] = useState<ReadonlySet<ThreadBucket>>(new Set())
  const toggleGroup = (bucket: ThreadBucket) => {
    setCollapsedBuckets((prev) => {
      const next = new Set(prev)
      if (next.has(bucket)) next.delete(bucket)
      else next.add(bucket)
      return next
    })
  }

  // The flat keyboard order: the expanded groups' rows, in display order.
  const visibleThreads = useMemo(
    () => groups.flatMap((g) => (collapsedBuckets.has(g.bucket) ? [] : g.items)),
    [groups, collapsedBuckets],
  )
  const visibleIndexById = useMemo(() => new Map(visibleThreads.map((t, i) => [t.id, i])), [visibleThreads])

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

  // Which thread the reader pane is showing. Component state, not the URL:
  // it is a selection within this view, and `/app/inbox/$threadId` already
  // exists as the addressable form of "this one thread" for a link to point at.
  const [selectedThreadId, setSelectedThreadId] = useState<string | undefined>(undefined)

  // A selection is only meaningful while the selected thread is on screen. A
  // filter or page change that drops it must clear the reader rather than
  // leave it showing a thread the list no longer contains.
  useEffect(() => {
    if (selectedThreadId && !items.some((t) => t.id === selectedThreadId)) setSelectedThreadId(undefined)
  }, [items, selectedThreadId])
  const selectedThread = useMemo(
    () => items.find((t) => t.id === selectedThreadId),
    [items, selectedThreadId],
  )

  // Three panes select in place; below the breakpoint the reader is its own
  // route, which also keeps the mark-read side effect in exactly one place
  // (ThreadReader) either way.
  const openThread = (thread: InboxThreadSummary) => {
    if (threePane) {
      setSelectedThreadId(thread.id)
      return
    }
    void navigate({ to: '/app/inbox/$threadId', params: { threadId: thread.id } })
  }

  const nav = useListKeyboardNav({
    count: visibleThreads.length,
    onOpen: (index) => {
      const thread = visibleThreads[index]
      if (thread) openThread(thread)
    },
  })

  // The command bar's verbs. Both are honest per-thread mutations — the API
  // has no bulk endpoint, so "Mark all as read" is the loaded page's unread
  // threads, one call each, and a partial failure is reported rather than
  // silently leaving some rows unread.
  const [setRead] = useSetInboxThreadReadMutation()
  const toggleRead = (thread: InboxThreadSummary) => {
    void setRead({ id: thread.id, setInboxThreadReadRequest: { unread: !thread.unread } })
  }
  const unreadOnPage = useMemo(() => items.filter((t) => t.unread).length, [items])
  const [markingAll, setMarkingAll] = useState(false)
  const [markAllFailures, setMarkAllFailures] = useState(0)
  const markAllRead = async () => {
    const unreadThreads = items.filter((t) => t.unread)
    if (unreadThreads.length === 0 || markingAll) return
    setMarkingAll(true)
    setMarkAllFailures(0)
    const results = await Promise.allSettled(
      unreadThreads.map((t) => setRead({ id: t.id, setInboxThreadReadRequest: { unread: false } }).unwrap()),
    )
    setMarkAllFailures(results.filter((r) => r.status === 'rejected').length)
    setMarkingAll(false)
  }

  const showError = error !== undefined && !staleCursor
  const hasActiveFilter = Boolean(selectedMailbox || replyClass || search.q || search.scope || selectedLabel)
  const isEmptyInbox = !isLoading && !showError && items.length === 0 && !hasActiveFilter && !search.cursor
  const isEmptyFiltered = !isLoading && !showError && items.length === 0 && !isEmptyInbox

  const listLabel = selectedMailbox
    ? mailboxLabel(selectedMailbox)
    : selectedLabel
      ? (labels.find((l) => l.id === selectedLabel)?.name ?? 'Category')
      : SCOPE_LABELS[scope]

  return (
    <Page>
      <PageTopbar eyebrow="Inbox" subtitle="Read + triage replies across every connected mailbox" />

      <CommandBar
        onCompose={() => setComposing(true)}
        selected={threePane ? selectedThread : undefined}
        onToggleRead={toggleRead}
        unreadOnPage={unreadOnPage}
        markAllBusy={markingAll}
        onMarkAllRead={() => void markAllRead()}
      />

      {markAllFailures > 0 && (
        <p role="alert" className="border-b border-border px-4 py-1.5 text-xs text-danger">
          {markAllFailures === 1
            ? "1 thread couldn't be marked as read — it stays unread."
            : `${markAllFailures} threads couldn't be marked as read — they stay unread.`}
        </p>
      )}

      <PageBody className="flex flex-col overflow-hidden lg:flex-row">
        <FolderPane
          overview={overview}
          overviewError={overviewError}
          scope={scope}
          mailboxes={mailboxOptions}
          mailboxesError={mailboxesError}
          selectedMailbox={selectedMailbox}
          labels={labels}
          selectedLabel={selectedLabel}
          onSelectScope={selectScope}
          onSelectMailbox={selectMailbox}
          onSelectLabel={selectLabel}
        />

        {/* The message list. A fixed column beside the reader (which is always
            present at three-pane width, showing its prompt when nothing is
            selected — the way a mail client keeps its reading pane on screen),
            full-width when the viewport only has room for the list. */}
        <div className={cn('flex min-w-0 flex-col', threePane ? 'w-[24rem] shrink-0 border-r border-border' : 'flex-1')}>
          <SectionBar label={listLabel}>
            <ListSearchInput value={typedQuery} onChange={setTypedQuery} placeholder="Search subject or contact email…" />
            <ReplyFilterMenu options={replyClassFilters} value={replyClass} onChange={selectClass} />
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
                description="Nothing matches the selected folder, mailbox, reply-class filter, and search."
                action={
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => {
                      resetPaging()
                      patch({
                        mailbox: undefined,
                        scope: undefined,
                        label: undefined,
                        class: undefined,
                        q: undefined,
                        cursor: undefined,
                      })
                      setTypedQuery('')
                    }}
                  >
                    Clear filters
                  </Button>
                }
              />
            </PageBody>
          ) : (
            <>
              <div
                ref={nav.containerRef}
                aria-busy={busy}
                className={cn('flex-1 overflow-y-auto transition-opacity', busy && 'opacity-50')}
              >
                <ThreadList
                  groups={groups}
                  collapsed={collapsedBuckets}
                  onToggleGroup={toggleGroup}
                  visibleIndexById={visibleIndexById}
                  mailboxLabel={mailboxLabel}
                  nav={nav}
                  onOpen={openThread}
                  onToggleRead={toggleRead}
                  selectedThreadId={selectedThreadId}
                />
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

        {/* The reader pane, only where there is room for it. Below the
            breakpoint `openThread` navigates instead, so nothing is
            unreachable — see openThread. */}
        {threePane && (
          <section aria-label="Thread" className="hidden min-w-0 flex-1 flex-col overflow-hidden lg:flex">
            {selectedThreadId ? (
              <>
                <div className="border-b border-border px-5 py-2.5">
                  <ThreadReaderHeading threadId={selectedThreadId} />
                </div>
                <div className="flex min-h-0 flex-1 overflow-hidden px-5 py-4">
                  {/* Keyed on the thread id, which is load-bearing rather than
                      cosmetic: the reader owns a ReplyComposer holding typed
                      text in local state, and this pane (unlike the standalone
                      route) stays mounted across thread switches. Without the
                      key, React would reconcile the same composer and a reply
                      drafted for one contact would post to whichever thread
                      was selected when Send was pressed. */}
                  <ThreadReader key={selectedThreadId} threadId={selectedThreadId} withContextPanel />
                </div>
              </>
            ) : (
              <ReaderPrompt />
            )}
          </section>
        )}
      </PageBody>

      {composing && <ComposeWindow onClose={() => setComposing(false)} />}
    </Page>
  )
}

/**
 * The reading pane before anything is selected — a mail client keeps the pane
 * on screen and says what it's for, rather than collapsing the column.
 */
function ReaderPrompt() {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8">
      <span className="flex size-20 items-center justify-center rounded-full bg-surface-2">
        <MailOpen className="size-8 text-faint" strokeWidth={1.5} aria-hidden="true" />
      </span>
      <div className="text-center">
        <p className="text-sm font-medium text-foreground">Select an item to read</p>
        <p className="mt-0.5 text-[12.5px] text-muted-foreground">Nothing is selected</p>
      </div>
    </div>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1, 2, 3, 4].map((i) => (
        <li key={i} className="flex items-start gap-2.5 border-b border-border py-2.5 pr-3 pl-4">
          <Skeleton className="size-8 shrink-0 rounded-full" />
          <div className="flex-1 space-y-2 pt-0.5">
            <Skeleton className="h-3.5 w-40" />
            <Skeleton className="h-2.5 w-56" />
          </div>
          <Skeleton className="h-3 w-12" />
        </li>
      ))}
    </ul>
  )
}
