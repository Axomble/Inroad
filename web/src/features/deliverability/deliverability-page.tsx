import { lazy, Suspense } from 'react'
import { AlertCircle } from 'lucide-react'
import { Skeleton } from '@/components/ui/skeleton'
import { Page, PageTopbar, PageBody, SectionBar } from '@/components/layout/page'
import { reportErrorMessage } from '@/lib/deliverability-copy'
import { useGetWorkspaceDeliverabilityQuery } from './api'
import { ScorePanel } from './score-panel'
import { AtRiskPanel } from './at-risk-panel'

// The chart is the only heavy part of this screen and nothing above it depends on
// it, so it loads after the score does. The route is code-split by the TanStack
// vite plugin; this splits the chart out of the route chunk as well.
const DeliverabilityChart = lazy(() => import('./deliverability-chart'))

/**
 * Workspace deliverability: the score, its components, the per-day series, and
 * what to go and fix.
 *
 * The screen's job is to be trusted, so it is built to under-claim. A failed load
 * renders as a failed load — never as an empty dashboard, which would read as
 * "all clear" — and every number that came from a signal nobody measured says so
 * (the rules live in `lib/deliverability-copy` and `./deliverability-series`).
 */
export function DeliverabilityPage() {
  const { data, isLoading, error } = useGetWorkspaceDeliverabilityQuery()

  return (
    <Page>
      <PageTopbar
        eyebrow="Deliverability"
        title="Deliverability"
        subtitle="Bounces and spam placement are the live inputs"
      />

      {isLoading ? (
        <LoadingScore />
      ) : error ? (
        // Deliberately the whole body: a score panel showing zeros beside an
        // error banner is worse than no panel, because the zeros look like data.
        <PageBody>
          <p role="alert" className="flex items-start gap-2 px-4 py-6 text-sm text-danger sm:px-6">
            <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
            <span>{reportErrorMessage(error)}</span>
          </p>
        </PageBody>
      ) : data ? (
        <PageBody>
          <ScorePanel score={data.score} />

          <section aria-label="Per-day signals" className="border-b border-border">
            <SectionBar label="Per-day signals" count={`${data.series.length} days`} />
            <Suspense
              fallback={
                <div className="grid gap-4 px-4 py-4 md:grid-cols-2 sm:px-5">
                  <Skeleton className="h-32 w-full" />
                  <Skeleton className="h-32 w-full" />
                </div>
              }
            >
              <DeliverabilityChart series={data.series} />
            </Suspense>
          </section>

          <AtRiskPanel
            label="Mailboxes at risk"
            items={data.at_risk_mailboxes}
            to="/app/mailboxes"
            emptyCopy="No mailbox is currently dragging the score down."
          />
          <AtRiskPanel
            label="Domains at risk"
            items={data.at_risk_domains}
            to="/app/mailboxes"
            emptyCopy="No sending domain is currently dragging the score down."
          />
        </PageBody>
      ) : null}
    </Page>
  )
}

function LoadingScore() {
  return (
    <PageBody>
      <div className="space-y-3 px-4 py-6 sm:px-6">
        <Skeleton className="h-16 w-40" />
        <Skeleton className="h-3.5 w-72" />
        <Skeleton className="h-3.5 w-56" />
      </div>
    </PageBody>
  )
}
