import { createFileRoute } from '@tanstack/react-router'
import { ContactsPage } from '@/features/contacts/contacts-page'
import { parseContactsSearch, type ContactsSearch } from '@/features/contacts/contacts-search'

/**
 * The whole contacts view lives in the URL — `?list=` (omitted means all
 * contacts), `?q=`, `?sort=`, `?cursor=`, `?limit=` — so a search is linkable and
 * survives a reload. `validateSearch` is authoritative: a param missing from this
 * validator is stripped on the next navigation, so the contract is defined once,
 * in `features/contacts/contacts-search.ts`, and applied here.
 */
export const Route = createFileRoute('/app/contacts')({
  validateSearch: (search: Record<string, unknown>): ContactsSearch => parseContactsSearch(search),
  component: ContactsPage,
})
