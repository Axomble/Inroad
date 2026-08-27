import { Link, Outlet, getRouteApi } from '@tanstack/react-router'
import { ArrowLeft } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill, StatusDot } from '@/components/shared/status-pill'
import { Page, PageTopbar, StatStrip, Stat, SectionBar } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { useGetCampaignQuery } from './api'
import { campaignTone, campaignLabel } from './status'
import { LifecycleMenu, CampaignStatusButton, PauseResumeDialog } from './lifecycle-menu'
import { usePauseResume } from './lifecycle-actions'
import { CampaignTabs } from './campaign-tabs'

const routeApi = getRouteApi('/app/campaigns/$id')

/**
 * One campaign's frame: identity, lifecycle controls, send counters, and the
 * tab strip — everything that is true of the campaign regardless of which
 * section is open. The active section renders into the `<Outlet/>` below.
 *
 * This used to be one page rendering all eight panels at once, which meant
 * opening a campaign to check its stats also mounted the sequence editor, the
 * schedule board, the senders table and the per-variant results breakdown, and
 * shipped all of them in one chunk. Splitting them into sibling routes lets the
 * router load only the section being looked at.
 *
 * The header lives HERE rather than in each child on purpose: it stays mounted
 * across tab navigation, so switching sections neither refetches the campaign
 * nor flashes a skeleton over the title. Children that need campaign fields
 * (metrics, tracking) call `useGetCampaignQuery` themselves and are served from
 * the RTK Query cache this component already populated — one request, not one
 * per tab.
 */
export function CampaignDetailLayout() {
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

      <CampaignTabs id={id} />

      {/* min-h-0 so a long section (the enrollments list, a 12-step sequence)
          scrolls inside itself rather than stretching the page past the frame. */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        <Outlet />
      </div>
    </Page>
  )
}
