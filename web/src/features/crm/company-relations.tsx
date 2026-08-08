import { Link } from '@tanstack/react-router'
import { Skeleton } from '@/components/ui/skeleton'
import { crmErrorMessage } from './error-copy'
import { useCrmListCompanyContactsQuery, useCrmListCompanyDealsQuery, type CrmCompanyContact } from './api'
import { DealRow } from './deal-row'
import { listPageSize } from '@/features/records/query-args'
import { MutedEmpty, QueryErrorBanner, Section } from '@/components/shared/record-page'


/**
 * A company's related records. Both are keyset-paginated sub-resources rather
 * than embedded lists, because a company's roster is genuinely unbounded — so
 * both ask for the server's ceiling and say plainly when a page was capped.
 */

export function CompanyContactsPanel({ companyId }: { companyId: string }) {
  const query = useCrmListCompanyContactsQuery({ id: companyId, limit: listPageSize })
  const contacts = query.data?.items ?? []

  return (
    <Section title="People" description="Contacts who belong to this account.">
      {query.isLoading ? <Skeleton className="h-16 w-full" aria-label="Loading contacts" /> : null}
      {query.isError ? (
        <QueryErrorBanner
          className=""
          message={crmErrorMessage(query.error, "This company's contacts could not be loaded.")}
          onRetry={() => void query.refetch()}
          retrying={query.isFetching}
        />
      ) : null}
      {contacts.length > 0 ? (
        <ul className="space-y-2">
          {contacts.map((contact) => <ContactRow key={contact.id} contact={contact} />)}
        </ul>
      ) : null}
      {/* An empty roster and a failed read are different answers. */}
      {!query.isLoading && !query.isError && contacts.length === 0 ? (
        <MutedEmpty text="No contacts are linked to this company yet." />
      ) : null}
      {query.data?.next_cursor !== undefined ? (
        <p role="status" className="pt-2 text-xs text-muted-foreground">
          Showing the first {contacts.length} contacts at this company. More exist.
        </p>
      ) : null}
    </Section>
  )
}

function ContactRow({ contact }: { contact: CrmCompanyContact }) {
  const name = [contact.first_name, contact.last_name].filter(Boolean).join(' ')
  return (
    <li className="flex min-w-0 flex-wrap items-center justify-between gap-2 rounded-lg border border-border bg-background p-3">
      <div className="min-w-0">
        <Link
          to="/app/contacts/$id"
          params={{ id: contact.id }}
          className="break-all text-sm font-medium text-foreground underline-offset-2 hover:text-accent-ink hover:underline"
        >
          {name || contact.email}
        </Link>
        {/* When the name is the link, the address is still worth showing — it is
            what the send path uses. */}
        <p className="mt-1 break-all text-xs text-muted-foreground">{name ? contact.email : 'No name on file'}</p>
      </div>
      {contact.job_title ? <p className="text-xs text-muted-foreground">{contact.job_title}</p> : null}
    </li>
  )
}

export function CompanyDealsPanel({ companyId }: { companyId: string }) {
  const query = useCrmListCompanyDealsQuery({ id: companyId, limit: listPageSize })
  const deals = query.data?.items ?? []

  return (
    <Section title="Deals" description="Opportunities on this account, in board order.">
      {query.isLoading ? <Skeleton className="h-16 w-full" aria-label="Loading deals" /> : null}
      {query.isError ? (
        <QueryErrorBanner
          className=""
          message={crmErrorMessage(query.error, "This company's deals could not be loaded.")}
          onRetry={() => void query.refetch()}
          retrying={query.isFetching}
        />
      ) : null}
      {deals.length > 0 ? (
        <ul className="space-y-2">
          {deals.map((deal) => <DealRow key={deal.id} deal={deal} />)}
        </ul>
      ) : null}
      {!query.isLoading && !query.isError && deals.length === 0 ? (
        <MutedEmpty text="No deals on this account yet." />
      ) : null}
      {query.data?.next_cursor !== undefined ? (
        <p role="status" className="pt-2 text-xs text-muted-foreground">
          Showing the first {deals.length} deals on this account. More exist.
        </p>
      ) : null}
    </Section>
  )
}
