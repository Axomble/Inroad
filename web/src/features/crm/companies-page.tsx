import { memo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { crmErrorMessage } from './error-copy'
import { Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { EmptyBlock, Page, PageBody, PageTopbar, Stat, StatStrip } from '@/components/layout/page'
import { useCrmListCompaniesQuery, type CrmCompany } from './api'
import { CompanyForm } from './company-form'
import { formatMoney } from '@/lib/money'
import { listPageSize } from '@/features/records/query-args'
import { QueryErrorBanner, TruncationNotice } from '@/components/shared/record-page'


// `data?.items ?? []` would hand every render a fresh array while the query is
// uninitialised or erroring, which defeats the `memo()` on the list.
const noCompanies: readonly CrmCompany[] = Object.freeze([])

/**
 * The Companies screen — one of the three CRM record types, and the account
 * layer contacts and deals hang off.
 *
 * This is the page the sidebar used to call "CRM": that name meant Companies
 * while the page itself opened on a deals tab, so Deals appeared twice in the
 * nav and Contacts sat outside the CRM. Deals and pipelines now live on
 * `/app/deals`, which is the single deals surface.
 */
export function CompaniesPage() {
  const [creating, setCreating] = useState(false)
  const companiesQuery = useCrmListCompaniesQuery({ limit: listPageSize })
  const companies = companiesQuery.data?.items ?? noCompanies
  // A cursor on the response means the server held records back.
  const truncated = companiesQuery.data?.next_cursor != null
  const withDomain = companies.filter(({ domain }) => Boolean(domain)).length
  const dealCount = companies.reduce((total, company) => total + company.deal_count, 0)
  const more = truncated ? '+' : ''

  return (
    <Page>
      <PageTopbar
        eyebrow="CRM"
        title="Companies"
        subtitle="Accounts, the people at them, and the deals they own."
        actions={
          <Button variant="primary" size="sm" onClick={() => setCreating((open) => !open)} aria-expanded={creating}>
            <Plus aria-hidden="true" />
            New company
          </Button>
        }
      />

      <StatStrip>
        <Stat
          label="Companies"
          value={companiesQuery.isError ? '—' : `${companies.length}${more}`}
          sub="in this workspace"
        />
        <Stat
          label="Deals"
          value={companiesQuery.isError ? '—' : `${dealCount}${more}`}
          sub="linked to an account"
        />
        <Stat
          label="With a domain"
          value={companiesQuery.isError ? '—' : `${withDomain}${more}`}
          sub="matchable to a contact"
        />
      </StatStrip>

      {creating ? <CompanyForm onDone={() => setCreating(false)} /> : null}

      <PageBody>
        {companiesQuery.isError ? (
          <QueryErrorBanner
            message={crmErrorMessage(companiesQuery.error, "Companies could not be loaded.")}
            onRetry={() => void companiesQuery.refetch()}
            retrying={companiesQuery.isFetching}
          />
        ) : companiesQuery.isLoading ? (
          <LoadingRows />
        ) : (
          <CompaniesList companies={companies} truncated={truncated} />
        )}
      </PageBody>
    </Page>
  )
}

const CompaniesList = memo(function CompaniesList({
  companies,
  truncated,
}: {
  companies: readonly CrmCompany[]
  truncated: boolean
}) {
  if (companies.length === 0) {
    return (
      <EmptyBlock
        title="No companies yet"
        description="Create a company to connect deals and contacts to an account."
      />
    )
  }
  return (
    <div className="divide-y divide-border [content-visibility:auto]">
      {companies.map((company) => (
        <article key={company.id} className="grid min-h-16 grid-cols-[minmax(0,1fr)_auto] items-center gap-4 px-5 py-3 hover:bg-surface/60">
          <div className="min-w-0">
            <h2 className="truncate text-sm font-medium">
              {/* The row's whole point is to open the record, so the name is the
                  link — one keyboard stop per row, not a row-wide click target
                  a screen reader can't name. */}
              <Link
                to="/app/companies/$id"
                params={{ id: company.id }}
                className="rounded text-foreground underline-offset-2 hover:text-accent-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {company.name}
              </Link>
            </h2>
            <p className="truncate text-xs text-muted-foreground">{company.domain || 'No domain added'}</p>
          </div>
          <div className="text-right">
            <p className="font-mono text-xs tabular-nums">{company.deal_count} deals</p>
            <p className="text-[11px] text-muted-foreground">
              {company.annual_revenue_micros == null
                ? 'Revenue not set'
                : formatMoney(company.annual_revenue_micros, company.currency)}
            </p>
          </div>
        </article>
      ))}
      {truncated && <TruncationNotice noun="companies" shown={companies.length} />}
    </div>
  )
})

function LoadingRows() {
  return (
    <div className="space-y-3 p-5" aria-label="Loading companies">
      <Skeleton className="h-14 w-full" />
      <Skeleton className="h-14 w-full" />
      <Skeleton className="h-14 w-full" />
    </div>
  )
}
