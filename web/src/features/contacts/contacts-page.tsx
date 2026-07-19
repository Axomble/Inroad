import { useEffect, useState } from 'react'
import { Plus } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Page, PageTopbar, SectionBar, StatStrip, Stat, PageBody, EmptyBlock } from '@/components/layout/page'
import type { ImportResult } from '@/store/api'
import { useListListsQuery, useListContactsQuery } from './api'
import { NewListForm } from './new-list-form'
import { ImportCsvForm } from './import-csv-form'

export function ContactsPage() {
  const [showNewList, setShowNewList] = useState(false)
  const [selectedListId, setSelectedListId] = useState<string | null>(null)
  const [lastImport, setLastImport] = useState<ImportResult | null>(null)
  const { data: listsData, isLoading: listsLoading } = useListListsQuery()
  const lists = listsData ?? []

  // Land on the first list once lists have loaded, so the contacts pane isn't
  // empty on first visit. Depend on the stable query result (not the derived
  // `lists` array, which is a new reference every render).
  useEffect(() => {
    const first = listsData?.[0]?.id
    if (!selectedListId && first) setSelectedListId(first)
  }, [listsData, selectedListId])

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

      <StatStrip>
        <Stat label="Lists" value={lists.length} />
        <Stat label="Imported" value={lastImport?.imported ?? '—'} />
        <Stat label="Skipped" value={lastImport?.skipped ?? '—'} />
        <Stat label="Duplicates" value={lastImport?.duplicates ?? '—'} />
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
          ) : lists.length === 0 && !showNewList ? (
            <p className="p-4 text-xs text-muted-foreground">No lists yet.</p>
          ) : (
            <ul>
              {lists.map((list) => (
                <li key={list.id}>
                  <button
                    type="button"
                    onClick={() => setSelectedListId(list.id ?? null)}
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
  const { data, isLoading, refetch } = useListContactsQuery({ list: listId, limit, offset })
  const contacts = data ?? []

  useEffect(() => {
    setOffset(0)
  }, [listId])

  return (
    <>
      <SectionBar label={listName || 'List'} count={contacts.length}>
        <ImportCsvForm
          listId={listId}
          onImported={(result) => {
            onImported(result)
            refetch()
          }}
        />
      </SectionBar>

      <div className="flex-1 overflow-y-auto">
        {isLoading ? (
          <ul>
            {[0, 1, 2].map((i) => (
              <li key={i} className="flex items-center gap-4 border-b border-border px-5 py-3">
                <Skeleton className="h-3.5 w-64" />
              </li>
            ))}
          </ul>
        ) : contacts.length === 0 ? (
          <EmptyBlock
            title="No contacts in this list"
            description="Import a CSV with an email column to populate this list."
          />
        ) : (
          <ul>
            {contacts.map((c) => (
              <li key={c.id} className="flex items-center gap-4 border-b border-border px-5 py-2.5">
                <span className="truncate text-[13.5px] text-foreground">{c.email}</span>
                {c.first_name && <span className="truncate text-xs text-muted-foreground">{c.first_name}</span>}
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="flex items-center justify-end gap-2 border-t border-border px-5 py-2">
        <Button
          variant="ghost"
          size="sm"
          disabled={offset === 0}
          onClick={() => setOffset((o) => Math.max(0, o - limit))}
        >
          Previous
        </Button>
        <Button
          variant="ghost"
          size="sm"
          disabled={contacts.length < limit}
          onClick={() => setOffset((o) => o + limit)}
        >
          Next
        </Button>
      </div>
    </>
  )
}
