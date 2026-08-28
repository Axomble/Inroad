import { AlertCircle, Flame } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Skeleton } from '@/components/ui/skeleton'
import { Page, PageTopbar, StatStrip, Stat, PageBody, EmptyBlock } from '@/components/layout/page'
import { StatusDot } from '@/components/shared/status-pill'
import type { WarmupHealth } from '@/lib/warmup-health'
// Read-only cross-feature query-hook reuse is allowed (see features/campaigns/api.ts);
// cross-feature UI imports are not, which is why the per-mailbox warmup controls
// live here rather than being injected into the mailboxes page.
import { useListMailboxesQuery } from '@/features/mailboxes/api'
import { useGetWarmupOverviewQuery } from './api'
import { WarmupContentVersionsPanel } from './warmup-content-versions-panel'
import { WarmupIncidentsPanel } from './warmup-incidents-panel'
import { WarmupMailboxCard } from './warmup-mailbox-card'
import { WarmupObserversPanel } from './warmup-observers-panel'
import { WarmupSentinelsPanel } from './warmup-sentinels-panel'

export function WarmupPage() {
  const { data: overview, isLoading: overviewLoading, isError: overviewError } = useGetWarmupOverviewQuery()
  const { data: mailboxes = [], isLoading: mailboxesLoading } = useListMailboxesQuery()

  const entries = overview?.mailboxes ?? []
  const entryById = new Map(entries.map((m) => [m.mailbox_id, m]))
  const countHealth = (state: WarmupHealth) => entries.filter((m) => m.enabled && m.health_state === state).length
  const active = overview?.active ?? false
  const isLoading = overviewLoading || mailboxesLoading

  return (
    <Page>
      <PageTopbar eyebrow="Warmup" />

      {overviewError && (
        <div role="alert" className="flex items-center gap-2 border-b border-danger/30 bg-danger/10 px-5 py-2.5 text-xs text-danger">
          <AlertCircle className="size-4 shrink-0" aria-hidden="true" />
          <span>Couldn't load the warmup overview. Refresh the page to try again.</span>
        </div>
      )}

      {/*
        On overview-fetch error the numbers below are unknown, not zero. Show
        em-dashes instead of fabricated "0 / Idle" alongside the error banner,
        dim the strip, and hide it from assistive tech (the banner conveys the
        state) so we never present fake zeros as real data.
      */}
      <StatStrip className={cn(overviewError && 'opacity-40')} aria-hidden={overviewError || undefined}>
        <Stat
          label="Pool size"
          value={overviewError ? '—' : (overview?.pool_size ?? 0)}
          sub={overviewError ? undefined : active ? 'Exchanging mail' : 'Idle — needs 2+'}
        />
        <Stat label="Healthy" value={overviewError ? '—' : countHealth('healthy')} dot={<StatusDot tone="running" />} />
        <Stat label="Watch" value={overviewError ? '—' : countHealth('watch')} dot={<StatusDot tone="paused" />} />
        <Stat label="Needs evidence" value={overviewError ? '-' : countHealth('unknown')} />
        <Stat
          label="At risk"
          value={overviewError ? '—' : countHealth('throttled') + countHealth('paused')}
          dot={<StatusDot tone="failing" />}
        />
      </StatStrip>

      {!isLoading && !overviewError && !active && mailboxes.length > 0 && (
        <div className="flex items-start gap-2 border-b border-warm/30 bg-warm/10 px-5 py-2.5 text-xs text-warm">
          <Flame className="size-4 shrink-0" aria-hidden="true" />
          <span>
            Warmup needs at least 2 mailboxes to exchange mail. Enable warmup on another mailbox below to start
            building the pool.
          </span>
        </div>
      )}

      <PageBody>
        {isLoading ? (
          <LoadingRows />
        ) : mailboxes.length === 0 ? (
          <EmptyBlock
            title="No mailboxes to warm"
            description="Connect a mailbox first, then enable warmup on it here. Warmup builds sender reputation by exchanging low-volume mail between your own opted-in mailboxes."
          />
        ) : (
          <>
            {/*
              First of the three pool-level panels, and the most upstream: it says
              what this pool's measurement arrangement IS. Every card below it
              carries an evidence label, and "Peer-only" is a term rather than a
              sentence until something on the page explains it — including, most of
              all, in the ordinary case where no sentinel is designated and every
              row reads that way.

              Nothing is rendered when the overview failed to load, or on a build
              that does not report sentinels: `sentinel_count` is then undefined,
              and "no sentinel is designated" would describe a pool nobody
              described.
            */}
            <WarmupSentinelsPanel
              count={overview?.sentinel_count}
              oversized={overview?.sentinel_pool_oversized}
              share={overview?.sentinel_pool_share}
              pool={entries}
            />
            {/*
              Then the evidence the pool actually produced: this qualifies every
              figure below it — the placement rates on the cards, and the
              degradation the incidents panel correlates. It also keeps the
              correlation panel adjacent to the list whose members it names.
              Nothing is rendered when the overview failed to load:
              `discounted_observers` is then undefined, and "no mailbox stands
              out" beside a load error would claim a comparison nobody ran.
            */}
            <WarmupObserversPanel observers={overview?.discounted_observers} pool={entries} />
            {/*
              Above the list, because an incident is a statement about SEVERAL
              mailboxes: buried in one card's disclosure it would be four
              identical panels an operator has to diff by hand, which is the work
              it exists to remove. Nothing is rendered when the overview failed to
              load — `incidents` is then undefined, and "no shared cause found"
              beside a load error would claim a search nobody ran.
            */}
            <WarmupIncidentsPanel
              incidents={overview?.incidents}
              pool={entries}
              minPool={overview?.incidents_min_pool}
            />
            {/*
              Last of the pool-level panels and directly above the list, because it
              is the one that says a bad number below might not be about a mailbox
              at all. An operator reading the cards has already started attributing
              spam to senders; this is the alternative explanation, and it has to be
              in view at that moment rather than above three other panels.

              Nothing is rendered when the overview failed to load —
              `content_versions` is then undefined, and "nothing observed yet"
              beside a load error would claim a window nobody measured.
            */}
            <WarmupContentVersionsPanel versions={overview?.content_versions} />
            <ul>
              {mailboxes.map((m) => (
                <WarmupMailboxCard key={m.id ?? ''} mailbox={m} entry={entryById.get(m.id ?? '')} />
              ))}
            </ul>
          </>
        )}
      </PageBody>
    </Page>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1, 2].map((i) => (
        <li key={i} className="flex items-center gap-4 border-b border-border px-5 py-3.5">
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-56" />
            <Skeleton className="h-2.5 w-40" />
          </div>
          <Skeleton className="h-7 w-20" />
        </li>
      ))}
    </ul>
  )
}
