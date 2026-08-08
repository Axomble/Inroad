import { Link } from '@tanstack/react-router'
import { ArrowLeft } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Page, PageBody, PageTopbar, Stat, StatStrip } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { useCrmGetCompanyQuery } from './api'
import { ActivityPanel } from './activity-panel'
import { CompanyContactsPanel, CompanyDealsPanel } from './company-relations'
import { NotesPanel } from './notes-panel'
import { TasksPanel } from './tasks-panel'
import { useOpenTasks } from './use-open-tasks'
import { crmErrorMessage } from './error-copy'
import { formatDateTime } from '@/lib/datetime'
import { formatMoney } from './money'
import { Detail, RecordPageMessage, RecordPageSkeleton, Section } from './record-parts'

/**
 * A company as a hub: its own fields, the people at it, the deals it owns, and
 * the notes, tasks and activity attached to the account.
 */
export function CompanyDetailPage({ companyId }: { companyId: string }) {
  const companyQuery = useCrmGetCompanyQuery({ id: companyId })
  const { open: openTasks } = useOpenTasks('company', companyId)
  const company = companyQuery.data

  if (companyQuery.isLoading) return <RecordPageSkeleton label="Loading company" />
  // A failed request is not a deleted company: a 500 or an offline browser gets a
  // retry, not "this account is gone".
  if (companyQuery.isError && httpStatus(companyQuery.error) !== 404) {
    return (
      <RecordPageMessage
        title="This company could not be loaded"
        description={crmErrorMessage(companyQuery.error, 'Try again in a moment.')}
        action={<Button onClick={() => void companyQuery.refetch()} disabled={companyQuery.isFetching}>Try again</Button>}
      />
    )
  }
  if (!company) {
    return (
      <RecordPageMessage
        title="Company not found"
        description="It may have been removed or belong to another workspace."
        action={<Button asChild><Link to="/app/companies">Back to companies</Link></Button>}
      />
    )
  }

  return (
    <Page>
      <PageTopbar
        eyebrow="Company"
        title={company.name}
        subtitle={company.domain || 'No domain added'}
        actions={
          <Button asChild size="sm">
            <Link to="/app/companies"><ArrowLeft aria-hidden="true" />Companies</Link>
          </Button>
        }
      />
      <StatStrip>
        <Stat label="Deals" value={company.deal_count} sub="on this account" />
        <Stat
          label="Annual revenue"
          value={company.annual_revenue_micros == null ? '—' : formatMoney(company.annual_revenue_micros, company.currency)}
          sub={company.annual_revenue_micros == null ? 'Not set' : company.currency}
        />
        <Stat label="Next actions" value={openTasks.length} sub="Open or in progress" />
      </StatStrip>
      <PageBody>
        <div className="grid min-w-0 gap-5 p-4 sm:p-5 xl:grid-cols-[minmax(0,1.45fr)_minmax(18rem,0.75fr)]">
          <div className="min-w-0 space-y-5">
            <CompanyContactsPanel companyId={companyId} />
            <CompanyDealsPanel companyId={companyId} />
            <NotesPanel targetType="company" targetId={companyId} />
          </div>
          <aside className="min-w-0 space-y-5">
            <Section title="Account details" description="What this record holds about the company itself.">
              <dl className="grid gap-3 text-sm">
                <Detail
                  label="Domain"
                  value={
                    company.domain ? (
                      <a
                        href={`https://${company.domain}`}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="text-accent-ink underline-offset-2 hover:underline"
                      >
                        {company.domain}
                      </a>
                    ) : (
                      'Not set'
                    )
                  }
                />
                <Detail label="Currency" value={company.currency} />
                <Detail label="Added" value={formatDateTime(company.created_at)} />
              </dl>
            </Section>
            <TasksPanel targetType="company" targetId={companyId} />
            <ActivityPanel targetType="company" targetId={companyId} />
          </aside>
        </div>
      </PageBody>
    </Page>
  )
}
