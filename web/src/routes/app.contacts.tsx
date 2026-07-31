import { createFileRoute } from '@tanstack/react-router'
import { ContactsPage } from '@/features/contacts/contacts-page'
import { parseListSearch, type ListSearch } from '@/lib/list-search'

/**
 * `?list=` keeps the selected contact list in the URL, so a list is linkable and
 * survives a reload instead of always snapping back to the first one. `?q=` /
 * `?sort=` are the shared list filter — see `lib/list-search.ts`.
 */
type ContactsSearch = ListSearch & { list?: string }

export const Route = createFileRoute('/app/contacts')({
  validateSearch: (search: Record<string, unknown>): ContactsSearch => ({
    ...parseListSearch(search),
    list: typeof search.list === 'string' && search.list !== '' ? search.list : undefined,
  }),
  component: ContactsPage,
})
