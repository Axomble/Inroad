import { Suspense, lazy, useId, useState } from 'react'
import { Fingerprint, Flame, History, Loader2, Route, Settings2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import { httpStatus } from '@/lib/rtk-error'
import { laneMeta, toWarmupLane } from '@/lib/warmup-lane'
import type { Mailbox, WarmupMailbox } from '@/store/api'
import { HealthBadge } from '@/components/shared/health-badge'
import { LaneBadge } from '@/components/shared/lane-badge'
import { WarmupIdentityPanel } from './warmup-identity-panel'
import { WarmupSettingsForm } from './warmup-settings-form'
import { useGetMailboxWarmupQuery, useDisableMailboxWarmupMutation } from './api'

// The sparkline is the one non-trivial visual and is only needed for enrolled
// mailboxes, so it's split out and loaded on demand behind Suspense rather than
// shipped in the route's main chunk.
const WarmupSparkline = lazy(() => import('./warmup-sparkline'))

// The change history is opt-in per mailbox — same treatment, and for a second
// reason: it carries its own copy tables and its own request, neither of which a
// page of ten collapsed rows should pay for.
const WarmupTransitionsPanel = lazy(() => import('./warmup-transitions-panel'))

// The destination-route matrix is opt-in too, and carries the largest copy table
// on this screen. Its data rides on the detail query this card already issues, so
// opening it costs a chunk and no request.
const WarmupRoutesPanel = lazy(() => import('./warmup-routes-panel'))

/**
 * 0..1 fraction to a whole-percent string, e.g. 0.83 -> "83%".
 *
 * A rate that is positive but rounds to nothing reads as "<1%", never "0%".
 * Every rate on this row is read as evidence about whether a mailbox is clean, so
 * rounding a real signal down to a confident zero is the same false-clean reading
 * this screen keeps having to remove — just a smaller one.
 */
function formatPct(value: number | null): string {
  if (value == null) return 'Not measured'
  if (value > 0 && value < 0.005) return '<1%'
  return `${Math.round(value * 100)}%`
}

function disableErrorMessage(status?: number): string {
  if (status === 404) return 'This mailbox is no longer a warmup participant — refresh the page.'
  return "Couldn't disable warmup. Please try again."
}

/**
 * One mailbox's warmup state on the overview: health, today's ramp progress, the
 * 7-day placement rates (inbox, spam, and tabbed where a provider can see tabs at
 * all), and a 30-day sparkline for enrolled mailboxes;
 * an enable affordance for the rest. All enable/disable/settings actions live
 * here (the mailboxes feature can't import warmup UI), with inline error and
 * loading states throughout.
 */
export function WarmupMailboxCard({
  mailbox,
  entry,
}: {
  mailbox: Mailbox
  /** The overview row for this mailbox, present only when it's enrolled. */
  entry?: WarmupMailbox
}) {
  const id = mailbox.id ?? ''
  const enrolled = !!entry?.enabled
  const [editing, setEditing] = useState(false)
  const [showHistory, setShowHistory] = useState(false)
  const [showIdentity, setShowIdentity] = useState(false)
  const [showRoutes, setShowRoutes] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const historyId = useId()
  const identityId = useId()
  const routesId = useId()

  // Detail (series + full participant) is only needed for the sparkline and to
  // prefill the settings form, so it's skipped for non-participants.
  const { data: detail, isLoading: detailLoading } = useGetMailboxWarmupQuery({ id }, { skip: !enrolled })
  const [disable, { isLoading: disabling }] = useDisableMailboxWarmupMutation()

  async function onDisable() {
    setActionError(null)
    const res = await disable({ id })
    if ('error' in res) setActionError(disableErrorMessage(httpStatus(res.error)))
  }

  return (
    <li className="border-b border-border">
      <div className="flex flex-col items-stretch gap-3 px-4 py-3 sm:flex-row sm:items-center sm:gap-4 sm:px-5">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span className="truncate text-[13.5px] font-medium text-foreground">{mailbox.email}</span>
            {enrolled && entry ? (
              <>
                {/* Two independent axes, two deliberately different chip shapes:
                    reputation (pill) then pool eligibility (squared, axis named). */}
                <HealthBadge state={entry.health_state} reason={entry.health_reason} />
                <LaneBadge lane={entry.lane} />
              </>
            ) : (
              <span className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">Not warming</span>
            )}
          </div>
          {enrolled && entry && (
            <>
              <div className="mt-1 flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-[11px] text-muted-foreground">
                <RampProgress sent={entry.today_sent} target={entry.today_target} />
                <span>
                  inbox 7d <span className="tabular-nums text-foreground">{formatPct(entry.inbox_rate_7d)}</span>
                </span>
                <span>
                  spam 7d <span className="tabular-nums text-foreground">{formatPct(entry.spam_rate_7d)}</span>
                </span>
                <span className="tabular-nums">{entry.placement_sample_7d} observations</span>
                <TabbedPlacement rate={entry.tabbed_rate_7d} tabCapableSamples={entry.tab_capable_sample_7d} />
              </div>
              <LaneReason lane={entry.lane} reason={entry.lane_reason} />
            </>
          )}
        </div>

        <div className="flex shrink-0 flex-wrap items-center gap-2">
          {enrolled ? (
            <>
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setShowIdentity((v) => !v)}
                aria-expanded={showIdentity}
                aria-controls={identityId}
                aria-label={`Sending identity for ${mailbox.email}`}
              >
                <Fingerprint className="size-3.5" />
                Identity
              </Button>
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setShowRoutes((v) => !v)}
                aria-expanded={showRoutes}
                aria-controls={routesId}
                aria-label={`Destination routes for ${mailbox.email}`}
              >
                <Route className="size-3.5" />
                Routes
              </Button>
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setShowHistory((v) => !v)}
                aria-expanded={showHistory}
                aria-controls={historyId}
                aria-label={`Change history for ${mailbox.email}`}
              >
                <History className="size-3.5" />
                History
              </Button>
              <Button variant="outline" size="xs" onClick={() => setEditing((v) => !v)} aria-label={`Warmup settings for ${mailbox.email}`}>
                <Settings2 className="size-3.5" />
                Settings
              </Button>
              <Button variant="ghost" size="xs" disabled={disabling} onClick={() => void onDisable()}>
                {disabling && <Loader2 className="size-3.5 animate-spin" />}
                Disable
              </Button>
            </>
          ) : (
            <Button variant="warm" size="xs" onClick={() => setEditing(true)}>
              <Flame className="size-3.5" />
              Enable warmup
            </Button>
          )}
        </div>
      </div>

      {enrolled && (
        <div className="px-5 pb-3">
          {detailLoading ? (
            <Skeleton className="h-8 w-full" />
          ) : detail ? (
            <Suspense fallback={<Skeleton className="h-8 w-full" />}>
              <WarmupSparkline series={detail.series} />
            </Suspense>
          ) : null}
        </div>
      )}

      {/*
        The observed sending identity, on the same in-place disclosure the
        history uses. Collapsed by default because it is diagnostic detail — six
        facts that answer "why is this failing", a question nobody asks until the
        metrics above have already raised it — and because none of it gates
        anything, so it must not sit where a gating figure sits.
      */}
      <div id={identityId}>
        {enrolled && entry && showIdentity && <WarmupIdentityPanel identity={entry.identity} />}
      </div>

      {/*
        Where this mailbox's mail was delivered, split by destination. Collapsed
        for the same two reasons the identity panel is: it answers "which route
        is failing", a question raised only once the pooled rates above have
        already looked wrong, and it gates nothing — so it must not sit where a
        gating figure sits.
      */}
      <div id={routesId}>
        {enrolled && showRoutes && (
          <Suspense fallback={<div className="border-t border-border px-5 py-3"><Skeleton className="h-16 w-full" /></div>}>
            <WarmupRoutesPanel mailboxId={id} />
          </Suspense>
        )}
      </div>

      {/*
        The history sits on the mailbox's own row rather than in a page-level
        panel or a drawer: it explains THIS mailbox's two badges, so it belongs
        directly beneath them, and the row already uses in-place disclosure for
        settings. Keeping it collapsed also keeps the page to one request.
      */}
      <div id={historyId}>
        {enrolled && showHistory && (
          <Suspense fallback={<div className="border-t border-border px-5 py-3"><Skeleton className="h-16 w-full" /></div>}>
            <WarmupTransitionsPanel mailboxId={id} />
          </Suspense>
        )}
      </div>

      {actionError && (
        <div role="alert" className="px-5 pb-3 text-[11px] text-danger">
          {actionError}
        </div>
      )}

      {editing && (
        <WarmupSettingsForm
          mailboxId={id}
          participant={detail?.participant}
          onDone={() => setEditing(false)}
          onCancel={() => setEditing(false)}
        />
      )}
    </li>
  )
}

/**
 * The server's explanation of the current lane — what put the mailbox there and
 * what clears it (§7 of the phase-1 design requires the condition be named, not
 * scored). Rendered as a line rather than a `title` tooltip because a withheld
 * mailbox takes no new campaign leads, which is too consequential to hide behind
 * a hover; the lane's own tone ties the sentence to the chip above it, and the
 * screen-reader prefix names the axis for anyone who can't see that link.
 */
function LaneReason({ lane, reason }: { lane: string; reason: string }) {
  if (!reason) return null
  return (
    <p className={cn('mt-1 text-[11px] leading-snug', laneMeta[toWarmupLane(lane)].text)}>
      <span className="sr-only">Pool status: </span>
      {reason}
    </p>
  )
}

/**
 * What may honestly be said about a mailbox's tabbed-placement rate.
 *
 * `null` is not "no tabs". Tabs are structurally undetectable over IMAP — they do
 * not exist as a concept there — so an absent rate means nothing observing this
 * mailbox could report a category, and a percentage would claim a measurement
 * that never happened. A rate arriving with no tab-capable observations behind it
 * is the same absence in the shape of a contradiction (a fraction of a population
 * of zero), and is read the same way.
 *
 * A rate of 0 over a real sample is the opposite case and stays a measurement:
 * everything a provider could categorise landed in the primary inbox, which is
 * the only good news this metric can deliver.
 *
 * Both fields are optional in the contract, so `undefined` — a server that does
 * not report them yet — lands in the undetectable branch too, rather than
 * defaulting to a clean zero the way an omitted `lane` once defaulted to
 * "Proving" on every card.
 */
type TabbedReading = { detected: false } | { detected: true; pct: string; tabCapableSamples: number }

function tabbedReading(rate: number | null | undefined, tabCapableSamples: number | undefined): TabbedReading {
  const samples = tabCapableSamples ?? 0
  if (rate == null || samples <= 0) return { detected: false }
  return { detected: true, pct: formatPct(rate), tabCapableSamples: samples }
}

/**
 * The share of warmup mail that landed in a tab (Gmail Promotions and friends)
 * rather than the primary inbox — the number that stops a mailbox reporting a
 * 100% inbox rate on mail almost nobody opens.
 *
 * It carries its own sample count because its denominator is NOT the observations
 * figure beside it: only observations whose reader could have seen a tab count
 * here, so 35% and "40 observations" describe two different populations and must
 * not be left to be read as one.
 *
 * And it says it gates nothing, because it does: no threshold, lane or promotion
 * decision reads it (design §8 — gating on a signal invisible across a whole
 * provider class would make promotion unreachable for every SMTP mailbox). That
 * note is on both branches: beside a rate so a high one is never mistaken for the
 * reason a mailbox is throttled, and beside the absence so "not detectable" is
 * never read as a penalty for using IMAP.
 */
function TabbedPlacement({
  rate,
  tabCapableSamples,
}: {
  rate: number | null | undefined
  tabCapableSamples: number | undefined
}) {
  const reading = tabbedReading(rate, tabCapableSamples)
  return (
    <span data-slot="tabbed-placement">
      tabbed 7d{' '}
      {reading.detected ? (
        <>
          <span className="tabular-nums text-foreground">{reading.pct}</span>{' '}
          <span className="whitespace-nowrap tabular-nums">
            of {reading.tabCapableSamples.toLocaleString()} tab-capable
          </span>
        </>
      ) : (
        <span className="text-foreground">Not detectable — no partner could report a tab</span>
      )}
      {/* Wrapped as a unit: on a phone this line breaks, and "· gates" / "nothing"
          split across two lines reads worse than moving the whole note down. */}
      <span className="whitespace-nowrap text-faint"> · gates nothing</span>
    </span>
  )
}

/**
 * Today's ramp as sent/target with a thin fill bar. A paused participant has a
 * target of 0, which reads as an explicit "paused today" rather than 0/0.
 */
function RampProgress({ sent, target }: { sent: number; target: number }) {
  if (target <= 0) {
    return <span className="text-warn">paused today</span>
  }
  const pct = Math.min(1, sent / target)
  return (
    <span className="inline-flex items-center gap-2">
      <span className="tabular-nums text-foreground">
        {sent}/{target}
      </span>
      <span className="h-1 w-16 overflow-hidden rounded-full bg-surface-2" aria-hidden="true">
        <span className={cn('block h-full rounded-full bg-warm')} style={{ width: `${pct * 100}%` }} />
      </span>
    </span>
  )
}
