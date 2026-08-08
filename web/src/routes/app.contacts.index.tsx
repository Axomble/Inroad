import { createFileRoute, redirect } from '@tanstack/react-router'
import { ContactsPage } from '@/features/contacts/contacts-page'
import { parseContactsSearch, type ContactsSearch } from '@/features/contacts/contacts-search'

/**
 * The whole contacts view lives in the URL — `?list=` (omitted means all
 * contacts), `?q=`, `?sort=`, `?cursor=`, `?limit=` — so a search is linkable and
 * survives a reload. `validateSearch` is authoritative: a param missing from this
 * validator is stripped on the next navigation, so the contract is defined once,
 * in `features/contacts/contacts-search.ts`, and applied here.
 */
export const Route = createFileRoute('/app/contacts/')({
  validateSearch: (search: Record<string, unknown>): ContactsSearch => parseContactsSearch(search),
  /**
   * `?contact=<id>` used to select a contact and expand an activity strip on this
   * page; that person now has a record page of their own. The param is redirected
   * rather than dropped because agent conversations already stored in the database
   * contain those links, and a user can still scroll back to them.
   */
  beforeLoad: ({ search }) => {
    if (search.contact !== undefined) {
      throw redirect({ to: '/app/contacts/$id', params: { id: search.contact }, replace: true })
    }
  },
  component: ContactsPage,
})
