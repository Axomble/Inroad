import { useEffect, useState } from 'react'
import { Plus } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
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
import type { Contact, ImportResult } from '@/store/api'
import { httpStatus } from '@/lib/rtk-error'
import { useUrlState } from '@/hooks/use-url-state'
import { useListControls, byText, type SortOption } from '@/hooks/use-list-controls'
import { useListListsQuery, useListContactsQuery } from './api'
import { NewListForm } from './new-list-form'
import { ImportCsvForm } from './import-csv-form'

/**
 * Module scope — see the memoization note in `useListControls`.
 *
 * Only email and first name are sortable because that is all the API returns:
 * `Contact` in `api/openapi.yaml` exposes `id`, `email`, `first_name`. The
 * contacts table also stores `last_name` and `company` (the CSV importer fills
 * them), so a Company column is a contract change away, not a UI change.
 */
const SORTS: readonly SortOption<Contact>[] = [
  { id: 'email', label: 'Email', compare: byText((c) => c.email) },
  { id: 'name', label: 'Name', compare: byText((c) => c.first_name) },
]

export function ContactsPage() {
  const [showNewList, setShowNewList] = useState(false)
  // The selected list lives in the URL (`?list=`), so a list is linkable and
  // survives a reload instead of snapping back to the first one every visit.
  const [selectedListId, setSelectedListId] = useUrlState('list')
  const [lastImport, setLastImport] = useState<ImportResult | null>(null)
  const { data: listsData, isLoading: listsLoading, error: listsError } = useListListsQuery()
  const lists = listsData ?? []

  // Land on the first list once lists have loaded, so the contacts pane isn't
  // empty on first visit. Depend on the stable query result (not the derived
  // `lists` array, which is a new reference every render).
  useEffect(() => {
    const first = listsData?.[0]?.id
    if (!selectedListId && first) setSelectedListId(first)
  }, [listsData, selectedListId, setSelectedListId])

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
        The last three read as unknown ("—") until an import runs in this
        session, so their captions say what they'll describe rather than
        implying a zero.
      */}
      <StatStrip>
        <Stat label="Lists" value={lists.length} sub="in this workspace" />
        <Stat label="Imported" value={lastImport?.imported ?? '—'} sub="last import" />
        <Stat label="Skipped" value={lastImport?.skipped ?? '—'} sub="invalid rows" />
        <Stat label="Duplicates" value={lastImport?.duplicates ?? '—'} sub="already in list" />
      </StatStrip>

      <PageBody className="flex overflow-hidden">
        <div className="flex w-56 shrink-0 flex-col overflow-y-auto border-r border-border">
          {showNewList && (
            <NewListForm
              onDone={(id) => {
                setShowNewList(false)
                setSelectedListId(id)
              }}
              onCancel={() => setShowNewList(false)}
            />
          )}

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
            <ul>
              {lists.map((list) => (
                <li key={list.id}>
                  <button
                    type="button"
                    onClick={() => setSelectedListId(list.id ?? '')}
                    className={cn(
                      'block w-full truncate px-4 py-2 text-left text-[13px] text-muted-foreground transition-colors',
                      'hover:bg-surface-2 hover:text-foreground',
                      selectedListId === list.id && 'bg-surface-2 font-medium text-foreground',
                    )}
                  >
                    {list.name}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="flex min-w-0 flex-1 flex-col">
          {selectedListId ? (
            <ContactsPane
              listId={selectedListId}
              listName={lists.find((l) => l.id === selectedListId)?.name ?? ''}
              onImported={setLastImport}
            />
          ) : (
            <EmptyBlock
              title="No list selected"
              description="Create a list to start importing contacts, or select one from the left."
            />
          )}
        </div>
      </PageBody>
    </Page>
  )
}

function ContactsPane({
  listId,
  listName,
  onImported,
}: {
  listId: string
  listName: string
  onImported: (result: ImportResult) => void
}) {
  const [offset, setOffset] = useState(0)
  const limit = 50
  // Fetch one extra row so we can distinguish "exactly `limit` results" (no
  // next page) from "at least `limit`+1 results" (next page available). The
  // extra row is trimmed off before render.
  const { data, isLoading, error } = useListContactsQuery({ list: listId, limit: limit + 1, offset })
  const fetched = data ?? []
  const hasMore = fetched.length > limit
  const page = hasMore ? fetched.slice(0, limit) : fetched

  // Filtering applies to the loaded page, so the label below says so rather than
  // implying it searched the whole list. Server-side search is the fix when a
  // list outgrows one page; the `?q=` param is already in the right place for it.
  const controls = useListControls({
    items: page,
    searchFields: (c) => [c.email, c.first_name],
    sorts: SORTS,
  })

  useEffect(() => {
    setOffset(0)
  }, [listId])

  const firstRow = offset + 1
  const lastRow = offset + page.length

  return (
    <>
      <SectionBar label={listName || 'List'} count={page.length}>
        <ListSearchInput
          value={controls.query}
          onChange={controls.setQuery}
          placeholder="Filter this page…"
        />
        <ImportCsvForm listId={listId} onImported={onImported} />
      </SectionBar>

      {!isLoading && !error && page.length > 0 && (
        <ListHeader>
          <ListHeaderCell className="min-w-0 flex-1">Email</ListHeaderCell>
          <ListHeaderCell className="w-40">First name</ListHeaderCell>
        </ListHeader>
      )}

      <div className="flex-1 overflow-y-auto">
        {isLoading ? (
          <ul>
            {[0, 1, 2].map((i) => (
              <li key={i} className="flex items-center gap-4 border-b border-border px-5 py-3">
                <Skeleton className="h-3.5 w-64" />
              </li>
            ))}
          </ul>
        ) : error ? (
          <div role="alert" className="px-5 py-6 text-sm text-danger">
            Couldn't load contacts{httpStatus(error) ? ` (${httpStatus(error)})` : ''} — try again.
          </div>
        ) : page.length === 0 ? (
          <EmptyBlock
            title="No contacts in this list"
            description="Import a CSV with an email column to populate this list."
          />
        ) : controls.items.length === 0 ? (
          <EmptyBlock
            title="No contacts match this filter"
            description={`Nothing on this page matches "${controls.query}".`}
            action={
              <Button variant="secondary" size="sm" onClick={controls.clear}>
                Clear filter
              </Button>
            }
          />
        ) : (
          <ul>
            {controls.items.map((c) => (
              <li key={c.id} className="flex items-center gap-4 border-b border-border px-5 py-2.5">
                <span className="min-w-0 flex-1 truncate text-[13.5px] text-foreground">{c.email}</span>
                <span className="w-40 truncate text-xs text-muted-foreground">{c.first_name || '—'}</span>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="flex items-center gap-2 border-t border-border px-5 py-2">
        {/*
          The API returns a page, not a total, so this is an honest "showing
          1–50" rather than a "1–50 of 248" we can't actually substantiate.
        */}
        <span className="font-mono text-[11px] tabular-nums text-faint">
          {page.length === 0 ? 'No contacts' : `Showing ${firstRow}–${lastRow}`}
          {controls.isFiltered && ` · ${controls.items.length} matching`}
        </span>
        <div className="ml-auto flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            disabled={offset === 0}
            onClick={() => setOffset((o) => Math.max(0, o - limit))}
          >
            Previous
          </Button>
          <Button variant="ghost" size="sm" disabled={!hasMore} onClick={() => setOffset((o) => o + limit)}>
            Next
          </Button>
        </div>
      </div>
    </>
  )
}
