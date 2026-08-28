import { useEffect, useState } from 'react'
import { Plus } from 'lucide-react'
import { Link, useSearch } from '@tanstack/react-router'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Page,
  PageTopbar,
  SectionBar,
  StatStrip,
  Stat,
  PageBody,
  EmptyBlock,
  ListHeader,
  ListHeaderCell,
} from '@/components/layout/page'
import { ListSearchInput } from '@/components/shared/list-search-input'
import { SortMenu } from '@/components/shared/sort-menu'
import type { Contact, ImportResult } from '@/store/api'
import { httpStatus } from '@/lib/rtk-error'
import { popCursor, pushCursor, type CursorStack } from '@/lib/cursor-stack'
import { useUrlState, useUrlPatch } from '@/hooks/use-url-state'
import { useDebouncedInput } from '@/hooks/use-debounced-input'
import { useListListsQuery, useListContactsQuery } from './api'
import {
  CONTACT_SORTS,
  MIN_QUERY_LENGTH,
  PAGE_SIZES,
  contactsErrorMessage,
  isStaleCursorError,
  isTooShort,
  limitOrDefault,
  parseContactsSearch,
  queryParam,
  rangeLabel,
  sortOrDefault,
} from './contacts-search'
import { NewListForm } from './new-list-form'
import { ImportCsvForm } from './import-csv-form'
import { ListRowActions } from './list-row-actions'

export function ContactsPage() {
  const [showNewList, setShowNewList] = useState(false)
  // The selected list lives in the URL (`?list=`), so a list is linkable and
  // survives a reload. No list selected is a real mode now — all contacts in the
  // workspace — so there is nothing to auto-select on first visit.
  const [selectedListId] = useUrlState('list')
  const patch = useUrlPatch()
  const [lastImport, setLastImport] = useState<ImportResult | null>(null)
  const { data: listsData, isLoading: listsLoading, error: listsError } = useListListsQuery()
  const lists = listsData ?? []

  // Switching scope invalidates the cursor: it points into the old result set.
  // One navigation, so the list can never land with a page-forty cursor.
  const selectList = (id: string) => patch({ list: id || undefined, cursor: undefined })

  return (
    <Page>
      <PageTopbar
        eyebrow="Contacts"
        actions={
          <Button variant="primary" size="sm" onClick={() => setShowNewList((v) => !v)}>
            <Plus className="size-4" />
            New list
          </Button>
        }
      />

      {/*
        Import counts exist only after an import runs in this session. Before
        one, a single labelled cell says so — three bare "—" stats read as a
        load failure, not as "nothing has happened yet".
      */}
      <StatStrip>
        <Stat label="Lists" value={lists.length} sub="in this workspace" />
        {lastImport ? (
          <>
            <Stat label="Imported" value={lastImport.imported ?? 0} sub="last import" />
            <Stat label="Skipped" value={lastImport.skipped ?? 0} sub="invalid rows" />
            <Stat label="Duplicates" value={lastImport.duplicates ?? 0} sub="already in list" />
          </>
        ) : (
          <Stat
            // Spans the three import columns; the border override beats the
            // strip's nth-child seam rule via tailwind-merge, not the cascade.
            className="md:col-span-3 md:[&:nth-child(2)]:border-r-0"
            label="Last import"
            value="—"
            sub="counts land here after your next CSV import"
          />
        )}
      </StatStrip>

      {lastImport && <ImportColumnReport result={lastImport} />}

      <PageBody className="flex flex-col overflow-hidden md:flex-row">
        <div className="flex max-h-40 w-full shrink-0 flex-col overflow-y-auto border-b border-border md:max-h-none md:w-56 md:border-b-0 md:border-r">
          {showNewList && (
            <NewListForm
              onDone={(id) => {
                setShowNewList(false)
                selectList(id)
              }}
              onCancel={() => setShowNewList(false)}
            />
          )}

          <ScopeButton
            label="All contacts"
            active={selectedListId === ''}
            onSelect={() => selectList('')}
          />

          {listsLoading ? (
            <div className="space-y-2 p-3">
              <Skeleton className="h-6 w-full" />
              <Skeleton className="h-6 w-full" />
              <Skeleton className="h-6 w-full" />
            </div>
          ) : listsError ? (
            <p role="alert" className="p-4 text-xs text-danger">
              Couldn't load lists{httpStatus(listsError) ? ` (${httpStatus(listsError)})` : ''} — try again.
            </p>
          ) : lists.length === 0 && !showNewList ? (
            <p className="p-4 text-xs text-muted-foreground">No lists yet.</p>
          ) : (
            <ul className="grid grid-cols-2 sm:grid-cols-3 md:block">
              {lists.map((list) => (
                <li key={list.id} className="group flex items-center">
                  <div className="min-w-0 flex-1">
                    <ScopeButton
                      label={list.name ?? 'Untitled list'}
                      count={list.contact_count}
                      active={selectedListId === list.id}
                      onSelect={() => selectList(list.id ?? '')}
                    />
                  </div>
                  {list.id && (
                    <ListRowActions
                      id={list.id}
                      name={list.name ?? 'Untitled list'}
                      // A deleted list can't stay the URL's scope: fall back to
                      // "All contacts" (which also drops the now-dangling cursor).
                      onDeleted={() => {
                        if (selectedListId === list.id) selectList('')
                      }}
                    />
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="flex min-w-0 flex-1 flex-col">
          <ContactsPane
            listId={selectedListId || undefined}
            listName={lists.find((l) => l.id === selectedListId)?.name ?? ''}
            onImported={setLastImport}
          />
        </div>
      </PageBody>
    </Page>
  )
}

/** One contact's cells, shared by the linked row and the id-less fallback. */
function ContactCells({ contact }: { contact: Contact }) {
  return (
    <>
      <span className="min-w-0 flex-1 truncate text-[13.5px] text-foreground">{contact.email}</span>
      <span className="hidden w-40 truncate text-xs text-muted-foreground sm:block">{contact.first_name || '—'}</span>
      <span className="hidden w-44 truncate text-xs text-muted-foreground md:block">{contact.company_name || '—'}</span>
      <span className="hidden w-16 text-right font-mono text-xs tabular-nums text-muted-foreground lg:block">
        {contact.deal_count ?? 0}
      </span>
    </>
  )
}

function ScopeButton({
  label,
  count,
  active,
  onSelect,
}: {
  label: string
  /**
   * Membership size, shown right-aligned. Omitted rather than zero where
   * there's no cheap number — "All contacts" would need a second aggregate,
   * and the nav's own doctrine is that a row with nothing truthful to show
   * carries no count.
   */
  count?: number
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
      {count != null && (
        <span className="shrink-0 font-mono text-[11px] tabular-nums text-faint">{count.toLocaleString()}</span>
      )}
    </button>
  )
}

function ContactsPane({
  listId,
  listName,
  onImported,
}: {
  listId: string | undefined
  listName: string
  onImported: (result: ImportResult) => void
}) {
  const search = parseContactsSearch(useSearch({ strict: false }))
  const patch = useUrlPatch()
  const sort = sortOrDefault(search.sort)
  const limit = limitOrDefault(search.limit)
  const cursor = search.cursor
  const appliedQuery = search.q

  // Keyset pagination can name the next page but not "page N back", so the pages
  // already visited are stacked as they're left. The stack is component state,
  // not URL state — it's this tab's history, and a pasted link legitimately
  // arrives without one.
  const [stack, setStack] = useState<CursorStack>([])
  const [recoveredFromStaleCursor, setRecoveredFromStaleCursor] = useState(false)

  // Any route back to the first page (Back, a new search, a list switch) also
  // empties the stack, so the "pages walked" count can't outlive the cursor it
  // describes.
  useEffect(() => {
    if (!cursor) setStack([])
  }, [cursor])

  const {
    data: page,
    currentData,
    isLoading,
    isFetching,
    error,
  } = useListContactsQuery(
    { list: listId, q: appliedQuery, sort, cursor, limit },
    // Contacts change underneath this view constantly — a CSV import, a teammate,
    // a worker suppressing a bounce — and none of that goes through a client
    // mutation, so no cache tag can ever invalidate it. Without this, returning to
    // a page size (or query, or cursor) visited earlier in the session replays the
    // cached response, showing a stale row set AND a stale `total`: 24 contacts
    // beside a pager that knows there are 154. Same reasoning, same fix as the
    // campaign enrollments list.
    { refetchOnMountOrArgChange: true },
  )

  // `data` is RTK Query's last successful result for this hook regardless of the
  // current args, `currentData` only the one matching them. Rendering `data`
  // is the keep-previous-page behaviour: the old rows stay up, dimmed, while the
  // next page loads — no skeleton flash and no layout jump per keystroke.
  const busy = isFetching && !currentData

  // Every control except the pager changes what is being searched, which makes
  // the current cursor meaningless — it points into the old result set. One
  // navigation, so the new query can never land on the old page forty.
  const applySearch = (next: { q?: string; sort?: string; limit?: number }) => {
    setRecoveredFromStaleCursor(false)
    setStack([])
    patch({ ...next, cursor: undefined })
  }

  const [typedQuery, setTypedQuery] = useDebouncedInput(appliedQuery ?? '', (next) =>
    applySearch({ q: queryParam(next) }),
  )

  // A cursor the server rejects (minted for another sort, or an encoding that
  // moved on) must not dead-end the list: drop it, reload page one, and say so.
  const staleCursor = cursor !== undefined && isStaleCursorError(error)
  useEffect(() => {
    if (!staleCursor) return
    setStack([])
    setRecoveredFromStaleCursor(true)
    patch({ cursor: undefined })
  }, [staleCursor, patch])

  const goNext = () => {
    const next = page?.next_cursor
    if (!next) return
    setRecoveredFromStaleCursor(false)
    setStack((s) => pushCursor(s, cursor))
    patch({ cursor: next })
  }

  const goPrev = () => {
    setRecoveredFromStaleCursor(false)
    const { stack: rest, cursor: back } = popCursor(stack)
    setStack(rest)
    // An empty stack means this tab never walked here — a link opened straight
    // onto a deep page — and the page's own `prev_cursor` is the only way back.
    // It is not the same as the stack *saying* "the first page", which is a
    // cursor-less `undefined` and must stay one.
    patch({ cursor: stack.length > 0 ? back : page?.prev_cursor ?? undefined })
  }

  const showError = error !== undefined && !staleCursor
  const items = page?.items ?? []
  // A cursor with no stack behind it is a pasted deep link: this tab never
  // walked here, so the row numbers are unknowable rather than zero.
  const pagesWalked = cursor !== undefined && stack.length === 0 ? null : stack.length
  const scopeLabel = listId ? listName || 'this list' : 'this workspace'

  return (
    <>
      <SectionBar label={listId ? listName || 'List' : 'All contacts'}>
        <ListSearchInput
          value={typedQuery}
          onChange={setTypedQuery}
          placeholder={listId ? 'Search this list…' : 'Search all contacts…'}
        />
        <SortMenu options={CONTACT_SORTS} value={sort} onChange={(id) => applySearch({ sort: id })} />
        {/*
          The shared Select, compacted to the 40px SectionBar: same native
          control underneath (keyboard model, typeahead, mobile picker), same
          themed chevron as every other select in the app.
        */}
        <Select
          aria-label="Contacts per page"
          wrapperClassName="w-auto"
          className="h-7 w-auto border-border bg-surface py-0 pl-2 pr-7 text-[12.5px] shadow-none"
          value={limit}
          onChange={(e) => applySearch({ limit: Number(e.target.value) })}
        >
          {PAGE_SIZES.map((size) => (
            <option key={size} value={size}>
              {size} / page
            </option>
          ))}
        </Select>
        {listId && <ImportCsvForm listId={listId} onImported={onImported} />}
      </SectionBar>

      {isTooShort(typedQuery) && (
        <p role="status" className="border-b border-border px-5 py-1.5 text-xs text-muted-foreground">
          Type at least {MIN_QUERY_LENGTH} characters to search.
        </p>
      )}

      {recoveredFromStaleCursor && (
        <p role="status" className="border-b border-border px-5 py-1.5 text-xs text-warn">
          That page link has expired — showing the first page.
        </p>
      )}

      {!isLoading && !showError && items.length > 0 && (
        <ListHeader>
          <ListHeaderCell className="min-w-0 flex-1">Email</ListHeaderCell>
          <ListHeaderCell className="hidden w-40 sm:block">First name</ListHeaderCell>
          <ListHeaderCell className="hidden w-44 md:block">Company</ListHeaderCell>
          <ListHeaderCell className="hidden w-16 text-right lg:block">Deals</ListHeaderCell>
        </ListHeader>
      )}

      {/*
        No virtualization, deliberately: a page is at most 100 rows, so the DOM
        is nowhere near the bottleneck — the query was, and keyset paging is the
        fix. A virtualizer here would buy nothing and cost scroll restoration,
        find-in-page, and Ctrl+F. Don't add one reflexively.
      */}
      <div
        aria-busy={busy}
        className={cn('flex-1 overflow-y-auto transition-opacity', busy && 'opacity-50')}
      >
        {isLoading ? (
          <ul>
            {[0, 1, 2].map((i) => (
              <li key={i} className="flex items-center gap-4 border-b border-border px-5 py-3">
                <Skeleton className="h-3.5 w-64" />
              </li>
            ))}
          </ul>
        ) : showError ? (
          <div role="alert" className="px-5 py-6 text-sm text-danger">
            {contactsErrorMessage(error)}
          </div>
        ) : items.length === 0 ? (
          appliedQuery ? (
            <EmptyBlock
              title="No contacts match this search"
              description={`Nothing in ${scopeLabel} matches "${appliedQuery}".`}
              action={
                // Distinct from the field's own X, which shares the visible
                // word, so the two are separable by name.
                <Button
                  variant="secondary"
                  size="sm"
                  aria-label="Clear search and show all contacts"
                  onClick={() => applySearch({ q: undefined })}
                >
                  Clear search
                </Button>
              }
            />
          ) : (
            <EmptyBlock
              title={listId ? 'No contacts in this list' : 'No contacts yet'}
              description={
                listId
                  ? 'Import a CSV with an email column to populate this list.'
                  : 'Create a list and import a CSV with an email column to get started.'
              }
            />
          )
        ) : (
          <ul>
            {items.map((c) => (
              <li key={c.id ?? c.email} className="border-b border-border">
                {/* The row opens the contact's record page, where their company,
                    deals and engagement history live. It used to expand an
                    inline activity strip, which showed a fraction of that and
                    could not be linked to. */}
                {c.id ? (
                  <Link
                    to="/app/contacts/$id"
                    params={{ id: c.id }}
                    className="flex min-h-11 items-center gap-4 px-5 py-2.5 hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
                  >
                    <ContactCells contact={c} />
                  </Link>
                ) : (
                  <div className="flex min-h-11 items-center gap-4 px-5 py-2.5">
                    <ContactCells contact={c} />
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="flex items-center gap-2 border-t border-border px-4 py-2 sm:px-5">
        <span className="font-mono text-[11px] tabular-nums text-faint">
          {page && !showError ? rangeLabel(page, pagesWalked, limit) : 'No contacts'}
        </span>
        {/* Dimming alone would leave the state invisible to a screen reader. */}
        {busy && <span className="text-[11px] text-muted-foreground">Loading…</span>}
        <div className="ml-auto flex items-center gap-2">
          {/* outline, not ghost: these sit beside a text range label, and a ghost
              button with nothing to hover reads as more of that prose. Pagination
              is the one place on the page where the control has to look like one. */}
          <Button
            variant="outline"
            size="sm"
            aria-label="Previous page"
            disabled={cursor === undefined || busy}
            onClick={goPrev}
          >
            Previous
          </Button>
          <Button
            variant="outline"
            size="sm"
            aria-label="Next page"
            disabled={!page?.next_cursor || busy}
            onClick={goNext}
          >
            Next
          </Button>
        </div>
      </div>
    </>
  )
}

/**
 * What the last import did with the columns it did not recognise.
 *
 * This exists because the counts alone cannot answer the question an operator
 * actually has after an import: "where did my `industry` column go?" Before
 * custom fields, unknown headers were dropped in silence — the import reported a
 * clean success and the data simply was not there. Naming both the mapped and
 * the ignored columns turns that into something diagnosable in one glance.
 *
 * Renders nothing when there is nothing to say, so a plain email-only CSV does
 * not grow a banner about columns it never had.
 */
function ImportColumnReport({ result }: { result: ImportResult }) {
  const mapped = result.mapped_fields ?? []
  const ignored = result.ignored_columns ?? []
  const invalid = result.invalid_values ?? 0
  if (mapped.length === 0 && ignored.length === 0 && invalid === 0) return null

  return (
    <div role="status" className="space-y-1 border-b border-border px-4 py-3 text-xs sm:px-5">
      {mapped.length > 0 && (
        <p className="text-muted-foreground">
          Mapped to custom fields: {mapped.map((key) => <code key={key} className="font-mono">{key}</code>)
            .flatMap((el, i) => (i === 0 ? [el] : [', ', el]))}
        </p>
      )}
      {ignored.length > 0 && (
        <p className="text-muted-foreground">
          Ignored columns (no matching custom field):{' '}
          {ignored.join(', ')} — define one under Settings → Custom fields to import them next time.
        </p>
      )}
      {invalid > 0 && (
        <p className="text-muted-foreground">
          {invalid} value{invalid === 1 ? '' : 's'} didn’t match their field’s type and were left empty. The contacts
          still imported.
        </p>
      )}
    </div>
  )
}
