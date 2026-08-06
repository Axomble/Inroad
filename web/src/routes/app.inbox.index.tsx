import { createFileRoute } from '@tanstack/react-router'
import { InboxPage } from '@/features/inbox/inbox-page'
import { parseInboxSearch, type InboxSearch } from '@/features/inbox/inbox-search'

/**
 * The whole inbox view lives in the URL — `?mailbox=`, `?class=`, `?q=`,
 * `?cursor=` — so a filtered view is linkable and survives a reload.
 * `validateSearch` is authoritative: a param missing from this validator is
 * stripped on the next navigation, so the contract is defined once, in
 * `features/inbox/inbox-search.ts`, and applied here (matches
 * `routes/app.contacts.tsx`'s identical reasoning).
 */
export const Route = createFileRoute('/app/inbox/')({
  validateSearch: (search: Record<string, unknown>): InboxSearch => parseInboxSearch(search),
  component: InboxPage,
})
