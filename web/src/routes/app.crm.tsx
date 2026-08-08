import { createFileRoute, redirect } from '@tanstack/react-router'

/**
 * `/app/crm` used to be the console the sidebar called "CRM" while the page it
 * opened was really Companies. The record types now have honest routes
 * (`/app/contacts`, `/app/companies`, `/app/deals`), so this path stays only to
 * keep existing links, bookmarks and agent-emitted URLs working.
 *
 * `beforeLoad` redirects before anything renders, so there is no flash of an
 * empty page and no component to keep in the bundle.
 */
export const Route = createFileRoute('/app/crm')({
  beforeLoad: () => {
    throw redirect({ to: '/app/companies', replace: true })
  },
})
