import { Link } from '@tanstack/react-router'
import { AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill } from '@/components/shared/status-pill'
import {
  Page,
  PageTopbar,
  PageBody,
  StatStrip,
  Stat,
  SectionBar,
  EmptyBlock,
  ListHeader,
  ListHeaderCell,
} from '@/components/layout/page'
import { campaignTone, campaignLabel } from '@/features/campaigns/status'
import { useGetCampaignReportQuery, type CampaignRow } from './api'
import { reportErrorMessage } from './report-error'

/**
 * Cross-campaign performance: which campaign is actually working.
 *
 * The gap this fills is that every performance number in the app was scoped to
 * one campaign, so comparing them meant opening each in turn and remembering
 * the last one. This is the same figures, ranked, from a single request.
 *
 * Two things it deliberately does NOT do:
 *
 * - **It doesn't invent a window.** Figures are lifetime, matching the campaign
 *   detail page exactly. A windowed reply rate would need an "enrolled in the
 *   window" denominator no other screen uses, so the same campaign would show
 *   two different reply rates depending on where you looked.
 * - **It doesn't dress opens up as reliable.** Open tracking is indicative and
 *   labelled as such here, the same as everywhere else it appears.
 */
export function ReportsPage() {
  const { data, isLoading, error, refetch } = useGetCampaignReportQuery()

  const campaigns = data?.campaigns ?? []
  const totals = data?.totals

  return (
    <Page>
      <PageTopbar
        eyebrow="Reports"
        title="Campaign performance"
        subtitle="Lifetime totals, ranked by volume"
      />

      {/* An error replaces the numbers rather than sitting above them: a stat
          strip of zeros beside an error banner reads as "nothing is working",
          which is a different and much worse claim than "we couldn't load it". */}
      {error ? (
        <PageBody>
          <p role="alert" className="flex items-start gap-2 px-4 py-6 text-sm text-danger sm:px-6">
            <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
            <span>{reportErrorMessage(error)}</span>
          </p>
          <div className="px-4 sm:px-6">
            <Button variant="secondary" size="sm" onClick={() => void refetch()}>
              Try again
            </Button>
          </div>
        </PageBody>
      ) : (
        <>
          <StatStrip>
            <Stat label="Sent" value={isLoading ? '—' : (totals?.sent ?? 0).toLocaleString()} sub="messages, all campaigns" />
            <Stat
              label="Replies"
              value={isLoading ? '—' : (totals?.replies ?? 0).toLocaleString()}
              sub={isLoading ? '' : `${formatRate(totals?.reply_rate)} of contacts`}
            />
            <Stat
              label="Opens"
              value={isLoading ? '—' : (totals?.opens ?? 0).toLocaleString()}
              sub={isLoading ? '' : `${formatRate(totals?.open_rate)} of sends · indicative`}
            />
            <Stat
              label="Bounces"
              value={isLoading ? '—' : (totals?.bounces ?? 0).toLocaleString()}
              sub={isLoading ? '' : `${formatRate(totals?.bounce_rate)} of contacts`}
            />
          </StatStrip>

          {!isLoading && campaigns.length === 0 ? (
            <PageBody>
              <EmptyBlock
                title="Nothing to compare yet"
                description="Once a campaign has sent, its performance shows up here beside every other campaign's."
                action={
                  <Button asChild variant="primary" size="sm">
                    <Link to="/app/campaigns">Go to campaigns</Link>
                  </Button>
                }
              />
            </PageBody>
          ) : (
            <>
              <SectionBar label="By campaign" count={isLoading ? undefined : campaigns.length}>
                <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
                  Lifetime
                </span>
              </SectionBar>

              <ListHeader>
                <ListHeaderCell className="min-w-0 flex-1">Campaign</ListHeaderCell>
                <ListHeaderCell className="w-20 text-right">Sent</ListHeaderCell>
                <ListHeaderCell className="hidden w-20 text-right sm:block">Open</ListHeaderCell>
                <ListHeaderCell className="hidden w-20 text-right md:block">Click</ListHeaderCell>
                <ListHeaderCell className="w-20 text-right">Reply</ListHeaderCell>
                <ListHeaderCell className="hidden w-20 text-right lg:block">Bounce</ListHeaderCell>
              </ListHeader>

              <PageBody>
                {isLoading ? (
                  <LoadingRows />
                ) : (
                  <ul>
                    {campaigns.map((campaign) => (
                      <CampaignReportRow key={campaign.id} campaign={campaign} />
                    ))}
                  </ul>
                )}
              </PageBody>
            </>
          )}
        </>
      )}
    </Page>
  )
}

/**
 * A rate as a percentage. One decimal below 10% — the difference between a
 * 0.4% and a 0.9% reply rate is the difference between a campaign working and
 * not, and "0%" vs "1%" hides it — whole numbers above, where the precision is
 * noise.
 */
function formatRate(rate: number | undefined): string {
  if (rate == null) return '—'
  const pct = rate * 100
  return `${pct > 0 && pct < 10 ? pct.toFixed(1) : Math.round(pct)}%`
}

function CampaignReportRow({ campaign }: { campaign: CampaignRow }) {
  return (
    <li className="border-b border-border last:border-b-0">
      <Link
        to="/app/campaigns/$id"
        params={{ id: campaign.id }}
        className="flex items-center gap-4 px-5 py-3 transition-colors hover:bg-surface-2/40"
      >
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13.5px] font-medium text-foreground">{campaign.name}</div>
          <div className="mt-0.5">
            <StatusPill tone={campaignTone(campaign.status)}>{campaignLabel(campaign.status)}</StatusPill>
          </div>
        </div>
        <Cell className="w-20">{campaign.sent.toLocaleString()}</Cell>
        <Cell className="hidden w-20 sm:block">{formatRate(campaign.open_rate)}</Cell>
        <Cell className="hidden w-20 md:block">{formatRate(campaign.click_rate)}</Cell>
        {/* The one column that gets emphasis: replies are what the sending is
            for, and the rest are leading indicators of it. */}
        <Cell className="w-20 font-medium text-foreground">{formatRate(campaign.reply_rate)}</Cell>
        <Cell className="hidden w-20 lg:block">{formatRate(campaign.bounce_rate)}</Cell>
      </Link>
    </li>
  )
}

function Cell({ className, children }: { className?: string; children: React.ReactNode }) {
  return (
    <div className={`shrink-0 text-right font-mono text-[12px] tabular-nums text-muted-foreground ${className ?? ''}`}>
      {children}
    </div>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1, 2, 3].map((i) => (
        <li key={i} className="border-b border-border px-5 py-3">
          <Skeleton className="h-9 w-full" />
        </li>
      ))}
    </ul>
  )
}
