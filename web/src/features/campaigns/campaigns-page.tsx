import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { Plus, Rocket } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill, StatusDot } from '@/components/shared/status-pill'
import { ListSearchInput } from '@/components/shared/list-search-input'
import { SortMenu } from '@/components/shared/sort-menu'
import {
  Page,
  PageTopbar,
  StatStrip,
  Stat,
  SectionBar,
  PageBody,
  EmptyBlock,
  ListHeader,
  ListHeaderCell,
  HintBar,
} from '@/components/layout/page'
import { cn } from '@/lib/utils'
import { httpStatus } from '@/lib/rtk-error'
import { useListControls, byText, byRank, type SortOption } from '@/hooks/use-list-controls'
import { useListKeyboardNav, LIST_NAV_HINTS } from '@/hooks/use-list-keyboard-nav'
// The email-verification gate is the auth feature's own concern — it owns both
// the state and the copy, so a call site names only the action.
import { VerifiedGateButton } from '@/features/auth/verified-gate-button'
import type { Campaign } from '@/store/api'
import { useToast } from '@/hooks/use-toast'
import { useListCampaignsQuery, useLaunchCampaignMutation } from './api'
import { campaignTone, campaignLabel } from './status'
import { CampaignForm } from './campaign-form'
import { LifecycleMenu, PauseResumeDialog } from './lifecycle-menu'
import { LAUNCH_GATED_ACTION, launchErrorMessage, usePauseResume } from './lifecycle-actions'
import { PreflightDialog } from './preflight-dialog'
import { StopClickBubble } from './stop-click-bubble'

/**
 * Module scope, not inline: `useListControls` memoises on the active
 * comparator's identity, so a fresh array each render would defeat the memo.
 *
 * "Needs attention first" leads because it is the ordering an operator actually
 * wants — a draft nobody launched and a running campaign are the two states you
 * act on, and `done` is the one you don't.
 */
const SORTS: readonly SortOption<Campaign>[] = [
  {
    id: 'attention',
    label: 'Needs attention',
    compare: byRank((c) => c.status, ['draft', 'running', 'paused', 'done']),
  },
  { id: 'name', label: 'Name', compare: byText((c) => c.name) },
  { id: 'status', label: 'Status', compare: byText((c) => c.status) },
]

export function CampaignsPage() {
  const [showForm, setShowForm] = useState(false)
  const { data: campaigns = [], isLoading, error: listError, refetch } = useListCampaignsQuery()
  const navigate = useNavigate()

  const controls = useListControls({
    items: campaigns,
    searchFields: (c) => [c.name, c.subject, c.status],
    sorts: SORTS,
  })

  const open = (campaign: Campaign) => {
    if (campaign.id) void navigate({ to: '/app/campaigns/$id', params: { id: campaign.id } })
  }

  const nav = useListKeyboardNav({
    count: controls.items.length,
    onOpen: (index) => {
      const campaign = controls.items[index]
      if (campaign) open(campaign)
    },
  })

  const statusCount = (status: string) => campaigns.filter((c) => c.status === status).length
  const isEmpty = campaigns.length === 0

  return (
    <Page>
      <PageTopbar
        eyebrow="Campaigns"
        actions={
          <Button variant="primary" size="sm" onClick={() => setShowForm((v) => !v)}>
            <Plus className="size-4" />
            New campaign
          </Button>
        }
      />

      <StatStrip>
        <Stat label="Total" value={listError ? '\u2014' : campaigns.length} sub="campaigns" />
        <Stat
          label="Running"
          value={listError ? '\u2014' : statusCount('running')}
          dot={<StatusDot tone="running" />}
          sub="sending now"
        />
        <Stat
          label="Draft"
          value={listError ? '\u2014' : statusCount('draft')}
          dot={<StatusDot tone="draft" />}
          sub="not launched"
        />
        <Stat
          label="Done"
          value={listError ? '\u2014' : statusCount('done')}
          dot={<StatusDot tone="done" />}
          sub="finished"
        />
      </StatStrip>

      {showForm && <CampaignForm onDone={() => setShowForm(false)} onCancel={() => setShowForm(false)} />}

      {!isEmpty && (
        <SectionBar
          label="All campaigns"
          count={controls.isFiltered ? `${controls.items.length}/${controls.totalCount}` : controls.totalCount}
        >
          <ListSearchInput
            value={controls.query}
            onChange={controls.setQuery}
            placeholder="Search campaigns…"
          />
          <SortMenu options={SORTS} value={controls.sortId} onChange={controls.setSortId} />
        </SectionBar>
      )}

      {isLoading ? (
        <PageBody>
          <LoadingRows />
        </PageBody>
      ) : listError ? (
        <PageBody>
          <EmptyBlock
            title="Couldn't load campaigns"
            description={`Your campaigns are still intact, but the server couldn't return them${httpStatus(listError) ? ` (${httpStatus(listError)})` : ''}. Try again in a moment.`}
            action={
              <Button variant="secondary" size="sm" onClick={() => void refetch()}>
                Try again
              </Button>
            }
          />
        </PageBody>
      ) : isEmpty ? (
        <PageBody>
          {!showForm && (
            <EmptyBlock
              title="No campaigns yet"
              description="Create a campaign from a connected mailbox to a contact list, then launch it to start sending."
              action={
                <Button variant="primary" size="sm" onClick={() => setShowForm(true)}>
                  <Plus className="size-4" />
                  New campaign
                </Button>
              }
            />
          )}
        </PageBody>
      ) : (
        <>
          <ListHeader>
            <ListHeaderCell className="min-w-0 flex-1">Campaign</ListHeaderCell>
            <ListHeaderCell className="hidden w-16 text-right md:block">Sent</ListHeaderCell>
            <ListHeaderCell className="w-24 text-right">Status</ListHeaderCell>
            {/* Wide enough for Launch + the overflow menu side by side — a
                narrower track pushed the buttons over the status column. */}
            <ListHeaderCell className="w-36 text-right">Actions</ListHeaderCell>
          </ListHeader>

          <PageBody ref={nav.containerRef}>
            {controls.items.length === 0 ? (
              <EmptyBlock
                title="No campaigns match this search"
                description={`Nothing matches "${controls.query}". Clear the search to see all ${controls.totalCount} campaigns.`}
                action={
                  <Button variant="secondary" size="sm" onClick={controls.clear}>
                    Clear search
                  </Button>
                }
              />
            ) : (
              <ul>
                {controls.items.map((campaign, index) => (
                  <CampaignRow
                    key={campaign.id}
                    campaign={campaign}
                    index={index}
                    active={nav.isActive(index)}
                    onHover={nav.onRowHover}
                    onOpen={open}
                  />
                ))}
              </ul>
            )}
          </PageBody>

          <HintBar hints={LIST_NAV_HINTS} />
        </>
      )}
    </Page>
  )
}

function CampaignRow({
  campaign,
  index,
  active,
  onHover,
  onOpen,
}: {
  campaign: Campaign
  index: number
  active: boolean
  onHover: (index: number) => void
  onOpen: (campaign: Campaign) => void
}) {
  const [launch, { isLoading }] = useLaunchCampaignMutation()
  const toast = useToast()
  const [error, setError] = useState<string | null>(null)
  const [preflightOpen, setPreflightOpen] = useState(false)
  // One instance per row, shared by LifecycleMenu's menu item and the
  // PauseResumeDialog it opens — this row has only the one trigger today, but
  // sharing keeps the row's wiring identical to the detail topbar's (which has
  // two triggers) rather than two different patterns for the same hook.
  const pauseResume = usePauseResume(campaign)
  const id = campaign.id ?? ''

  async function onLaunch() {
    setError(null)
    const res = await launch({ id })
    if ('error' in res) {
      setError(launchErrorMessage(res.error))
      return
    }
    // A launch is the one action here with consequences that outlive the
    // screen — mail starts going out. The status pill flipping to "running" is
    // easy to miss, and the row may well be scrolled away or navigated from by
    // the time it does, so the confirmation follows the user instead.
    toast.ok(`${campaign.name || 'Campaign'} is live — sending has started.`, {
      href: id ? `/app/campaigns/${id}` : undefined,
      hrefLabel: 'View campaign',
    })
  }

  return (
    <li
      data-row-index={index}
      className={cn(
        'flex cursor-pointer items-center gap-4 border-b border-border px-5 py-3 transition-colors',
        // The keyboard cursor and hover share one highlight, so "current" always
        // means the same thing however you got there.
        active ? 'bg-surface-2/60' : 'hover:bg-surface-2/40',
      )}
      onMouseEnter={() => onHover(index)}
      onClick={() => onOpen(campaign)}
    >
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13.5px] font-medium text-foreground">{campaign.name}</div>
        <div className="truncate font-mono text-[11px] text-faint">{campaign.subject}</div>
        {/* Its own line, not a suffix on the truncated subject: a launch failure
            now carries actionable copy (the verification gate's is a whole
            sentence), and truncation would swallow the actionable half. Mirrors
            MailboxRow's inline action error. */}
        {error && (
          <div role="alert" className="mt-0.5 text-[11px] text-danger">
            {error}
          </div>
        )}
      </div>

      <div className="hidden w-16 justify-end md:flex">
        <span className="font-mono text-xs tabular-nums text-muted-foreground">
          {(campaign.stats?.sent ?? 0).toLocaleString()}
        </span>
      </div>

      <div className="flex w-24 shrink-0 justify-end">
        <StatusPill tone={campaignTone(campaign.status)}>{campaignLabel(campaign.status)}</StatusPill>
      </div>

      <div className="flex w-36 shrink-0 items-center justify-end gap-1">
        {campaign.status === 'draft' && (
          <StopClickBubble>
            {/* POST /campaigns/{id}/launch is behind `auth.RequireVerified`. */}
            <VerifiedGateButton
              action={LAUNCH_GATED_ACTION}
              variant="secondary"
              size="xs"
              disabled={isLoading}
              onClick={() => setPreflightOpen(true)}
            >
              <Rocket className="size-3.5" />
              Launch
            </VerifiedGateButton>
            <PreflightDialog
              open={preflightOpen}
              onOpenChange={setPreflightOpen}
              campaignId={id}
              campaignName={campaign.name}
              onConfirm={() => void onLaunch()}
              isLaunching={isLoading}
            />
          </StopClickBubble>
        )}

        {/* Pause/resume/rename/delete, status-appropriate. "Open campaign" isn't
            duplicated here — clicking anywhere else on the row (or the
            keyboard nav's ↵) already opens it, and LifecycleMenu stops its own
            clicks from also bubbling into the row's onClick. */}
        <LifecycleMenu campaign={campaign} pauseResume={pauseResume} />
        <PauseResumeDialog campaign={campaign} pauseResume={pauseResume} />
      </div>
    </li>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1, 2].map((i) => (
        <li key={i} className="flex items-center gap-4 border-b border-border px-5 py-3.5">
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-48" />
            <Skeleton className="h-2.5 w-64" />
          </div>
          <Skeleton className="h-4 w-16" />
        </li>
      ))}
    </ul>
  )
}
