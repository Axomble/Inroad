import { Link, getRouteApi } from '@tanstack/react-router'
import { ArrowLeft } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill, StatusDot } from '@/components/shared/status-pill'
import { Page, PageTopbar, StatStrip, Stat, SectionBar, PageBody } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { useGetCampaignQuery } from './api'
import { campaignTone, campaignLabel } from './status'
import { LifecycleMenu, CampaignStatusButton, PauseResumeDialog } from './lifecycle-menu'
import { usePauseResume } from './lifecycle-actions'
import { MetricsPanel } from './metrics-panel'
import { ResultsPanel } from './results-panel'
import { CampaignEnrollmentsList } from './campaign-enrollments-list'
import { SequenceEditor } from './sequence-editor'
import { SchedulePanel } from './schedule-panel'
import { SendersPanel } from './senders-panel'
import { GuardrailsCard } from './guardrails-card'

const routeApi = getRouteApi('/app/campaigns/$id')

/**
 * One campaign, at its own address.
 *
 * This used to render inline above the campaign list from `useState`, which meant
 * a campaign had no URL (nothing to link or bookmark), Back didn't close it, and
 * opening one pushed the row you clicked off the bottom of the screen. Being a
 * route fixes all three, and the router code-splits this chunk so the sequence
 * editor and metrics panel aren't in the list route's bundle.
 */
export function CampaignDetailPage() {
  const { id } = routeApi.useParams()
  const { data, isLoading, error } = useGetCampaignQuery({ id })
  const stats = data?.stats ?? {}
  const n = (key: string) => stats[key] ?? 0
  // One shared controller for both pause/resume controls below — called
  // unconditionally (Rules of Hooks) even before `data` resolves; `data ?? {}`
  // is a valid (if id/status-less) Campaign in the meantime. See
  // `usePauseResume`'s doc comment for why this must not be two instances.
  const pauseResume = usePauseResume(data ?? {})

  return (
    <Page>
      <PageTopbar
        eyebrow="Campaign"
        back={
          <Button variant="ghost" size="icon-sm" asChild className="shrink-0">
            <Link to="/app/campaigns" aria-label="Back to all campaigns">
              <ArrowLeft className="size-4" />
            </Link>
          </Button>
        }
        // A skeleton, not the eyebrow word promoted to a title: "Campaign" as
        // the page name reads like a fetch that never happened.
        title={isLoading ? <Skeleton className="h-5 w-48" /> : data?.name}
        subtitle={data?.subject}
        actions={
          data?.status ? (
            <div className="flex items-center gap-2">
              <StatusPill tone={campaignTone(data.status)}>{campaignLabel(data.status)}</StatusPill>
              {/* Pause/resume gets its own visible button — the one action an
                  operator looking at this campaign is most likely to take —
                  while rename/delete stay in the overflow menu. Both share
                  `pauseResume` and the one `PauseResumeDialog` below, so they
                  can't stack a second confirm dialog or double-fire. */}
              <CampaignStatusButton campaign={data} pauseResume={pauseResume} />
              <LifecycleMenu campaign={data} pauseResume={pauseResume} />
              <PauseResumeDialog campaign={data} pauseResume={pauseResume} />
            </div>
          ) : undefined
        }
      />

      <SectionBar label="Sends" />

      {isLoading ? (
        <div className="px-5 py-4">
          <Skeleton className="h-6 w-64" />
        </div>
      ) : error ? (
        // A failed detail fetch must not masquerade as a real all-zero campaign
        // (the grid below would be indistinguishable from an empty one).
        <div role="alert" className="px-5 py-6 text-sm text-danger">
          Couldn't load campaign stats{httpStatus(error) ? ` (${httpStatus(error)})` : ''} — try again.
        </div>
      ) : (
        <StatStrip>
          <Stat label="Queued" value={n('queued')} dot={<StatusDot tone="draft" />} />
          <Stat label="Sent" value={n('sent')} dot={<StatusDot tone="running" />} sub="delivered to the provider" />
          <Stat label="Failed" value={n('failed')} dot={<StatusDot tone="failing" />} sub="permanent errors" />
          <Stat label="Skipped" value={n('skipped')} dot={<StatusDot tone="paused" />} sub="suppressed or capped" />
        </StatStrip>
      )}

      <PageBody>
        {/* The sequence is the campaign's definition — surface it first. Owns its
            own loading/empty/error states. */}
        <SequenceEditor campaignId={id} status={data?.status} />

        {/* When a campaign sends is as much its definition as what it sends, so
            the schedule sits directly under the steps. */}
        <SchedulePanel campaignId={id} />

        {/* Who it sends as belongs with when it sends: both shape every future
            send without touching threads already in flight. */}
        <SendersPanel campaignId={id} />

        {/* What will stop it. Sits directly under who/when it sends as, because a
            campaign that paused itself is answered here and nowhere else. Owns its
            own loading/error states. */}
        <GuardrailsCard campaignId={id} />

        {!isLoading && !error && (
          <MetricsPanel campaignId={id} metrics={data?.metrics} trackingEnabled={data?.tracking_enabled} />
        )}

        {/* The per-step, per-variant breakdown, directly under the campaign-wide
            rollup it decomposes: the rollup answers "is this working", this
            answers "which step and which copy". Owns its own loading/error
            states. */}
        <ResultsPanel campaignId={id} />

        {/* Contacts + their classified replies. Owns its own loading/empty/error
            states, so it mounts regardless of the campaign-detail query. */}
        <CampaignEnrollmentsList campaignId={id} />
      </PageBody>
    </Page>
  )
}
