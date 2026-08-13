import { Suspense, lazy, useId, useState } from 'react'
import { Flame, History, Loader2, Settings2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'
import { httpStatus } from '@/lib/rtk-error'
import { laneMeta, toWarmupLane } from '@/lib/warmup-lane'
import type { Mailbox, WarmupMailbox } from '@/store/api'
import { HealthBadge } from '@/components/shared/health-badge'
import { LaneBadge } from '@/components/shared/lane-badge'
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

/** 0..1 fraction to a whole-percent string, e.g. 0.83 -> "83%". */
function formatPct(value: number | null): string {
  if (value == null) return 'Not measured'
  return `${Math.round(value * 100)}%`
}

function disableErrorMessage(status?: number): string {
  if (status === 404) return 'This mailbox is no longer a warmup participant — refresh the page.'
  return "Couldn't disable warmup. Please try again."
}

/**
 * One mailbox's warmup state on the overview: health, today's ramp progress,
 * 7-day inbox-placement rate, and a 30-day sparkline for enrolled mailboxes;
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
  const [actionError, setActionError] = useState<string | null>(null)
  const historyId = useId()

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
