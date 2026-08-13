import { formatDateTime } from '@/lib/datetime'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import { healthMeta } from '@/lib/warmup-health'
import { laneMeta } from '@/lib/warmup-lane'
import { HealthBadge } from '@/components/shared/health-badge'
import { LaneBadge } from '@/components/shared/lane-badge'
import { InlineLoading, MoreExist, MutedEmpty, QueryErrorBanner } from '@/components/shared/record-page'
import type { WarmupTransition } from '@/store/api'
import { useListWarmupTransitionsQuery } from './api'
import { warmupErrorMessage } from './error-copy'
import {
  evidenceRows,
  healthChange,
  laneChange,
  laneReasonCopy,
  reasonCopy,
  type EvidenceRow,
  type HealthChange,
  type LaneChange,
} from './transition-copy'

/**
 * How many entries one mailbox shows. The engine writes a row per sweep on which
 * either axis moved, so a busy mailbox accumulates faster than anyone reads; the
 * recent past is what answers "why is it here", and `MoreExist` says plainly that
 * the list stops short rather than letting it read as the whole record.
 */
const HISTORY_LIMIT = 20

/**
 * The append-only record of every automated warmup decision for one mailbox,
 * newest first.
 *
 * Until now an operator saw only the current two badges, so the reasoning behind
 * them was invisible — and the reputation design's standing objection is that
 * nobody should be shown a bare score. Each entry answers three questions in the
 * order they are asked: what changed (each axis, from and to), why (the mapped
 * reason, never the raw code), and on what evidence (every rate as a bound, with
 * the sample it was judged on).
 *
 * Default export so the card can `React.lazy` it — the history is only fetched
 * and only shipped when an operator opens it, matching how the sparkline is
 * handled.
 */
export default function WarmupTransitionsPanel({ mailboxId }: { mailboxId: string }) {
  const history = useListWarmupTransitionsQuery({ mailboxId, limit: HISTORY_LIMIT })
  const transitions = history.data?.transitions ?? []

  return (
    <div className="border-t border-border bg-surface/40 px-4 py-3 sm:px-5">
      <p className="mb-2.5 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">Change history</p>

      {history.isLoading && <InlineLoading label="Loading change history" />}

      {history.isError && (
        <QueryErrorBanner
          className=""
          message={warmupErrorMessage(
            history.error,
            "This mailbox's change history couldn't be loaded, so nothing here is known.",
          )}
          onRetry={() => void history.refetch()}
          retrying={history.isFetching}
        />
      )}

      {!history.isLoading && !history.isError && transitions.length === 0 && (
        <MutedEmpty text="Nothing has happened yet. Every automated change to this mailbox's reputation or pool lane is recorded here with the evidence behind it, and a mailbox that has just joined the pool has nothing to show." />
      )}

      {transitions.length > 0 && (
        <ol className="divide-y divide-border">
          {transitions.map((transition) => (
            <TransitionEntry key={transition.id} transition={transition} />
          ))}
        </ol>
      )}

      {transitions.length >= HISTORY_LIMIT && <MoreExist noun="changes" />}
    </div>
  )
}

/**
 * One decision. The two axes get one line each, always both, because a row can
 * move either or both and collapsing them into a single status is the thing the
 * lane axis was introduced to stop.
 */
function TransitionEntry({ transition }: { transition: WarmupTransition }) {
  const health = healthChange(transition)
  const lane = laneChange(transition)
  const laneReason = laneReasonCopy(transition.lane_reason_code, transition.lane_reason)

  return (
    <li className="flex min-w-0 gap-3 py-3 first:pt-0 last:pb-0">
      {/* The rail node is toned by where the reputation axis landed; the badges
          below carry the same fact in words, so the colour adds nothing on its own. */}
      <span
        className={cn(
          'mt-1.5 size-1.5 shrink-0 rounded-full',
          healthMeta[health.kind === 'moved' ? health.to : health.state].dot,
        )}
        aria-hidden="true"
      />

      <div className="min-w-0 flex-1 space-y-2">
        <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
          <time dateTime={transition.created_at} className="font-mono text-[11px] text-muted-foreground">
            {formatDateTime(transition.created_at)}
            <span className="text-faint"> · {relativeTime(transition.created_at)}</span>
          </time>
          {/* Which thresholds decided this, so the row stays readable after they
              move. Labelled in text rather than a `title`, which no keyboard or
              touch reader ever sees. */}
          <span className="font-mono text-[10px] text-faint">
            <span className="opacity-70">policy </span>
            {transition.policy_version}
          </span>
        </div>

        <AxisLine axis="Reputation">
          <HealthAxis change={health} />
        </AxisLine>
        <Explanation text={reasonCopy(transition.reason_code, transition.reason)} />

        <AxisLine axis="Pool">
          <PoolAxis change={lane} />
        </AxisLine>
        {laneReason && <Explanation text={laneReason} tone={laneTone(lane)} />}

        <Evidence rows={evidenceRows(transition)} />
      </div>
    </li>
  )
}

/**
 * The lane's own tone, so its sentence ties back to the chip above it. An
 * unrecorded lane has no tone to borrow and keeps the neutral one.
 */
function laneTone(lane: LaneChange): string | undefined {
  if (lane.kind === 'unrecorded') return undefined
  return laneMeta[lane.kind === 'moved' ? lane.to : lane.lane].text
}

function HealthAxis({ change }: { change: HealthChange }) {
  if (change.kind === 'moved') {
    return (
      <>
        <HealthBadge state={change.from} />
        <Arrow />
        <HealthBadge state={change.to} />
      </>
    )
  }
  return (
    <>
      <HealthBadge state={change.state} />
      <Unchanged />
    </>
  )
}

/**
 * The pool axis, including the case the lane columns were added after: a row
 * written before lanes existed carries null on both ends. That is stated as the
 * absence it is — a chip here would invent a lane the engine never assigned.
 */
function PoolAxis({ change }: { change: LaneChange }) {
  if (change.kind === 'unrecorded') {
    return (
      <span className="text-[11px] text-muted-foreground">
        No pool lane was recorded — this entry predates pool lanes.
      </span>
    )
  }
  if (change.kind === 'moved') {
    return (
      <>
        <LaneBadge lane={change.from} />
        <Arrow />
        <LaneBadge lane={change.to} />
      </>
    )
  }
  return (
    <>
      <LaneBadge lane={change.lane} />
      <Unchanged />
    </>
  )
}

/**
 * One axis and its state, with the axis named in text. Naming it is what stops
 * two chips on adjacent lines from being read as one status.
 */
function AxisLine({ axis, children }: { axis: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <span className="w-[68px] shrink-0 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">{axis}</span>
      {children}
    </div>
  )
}

function Arrow() {
  return (
    <>
      <span className="sr-only">changed to</span>
      <span className="text-faint" aria-hidden="true">
        →
      </span>
    </>
  )
}

function Unchanged() {
  return <span className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">unchanged</span>
}

function Explanation({ text, tone }: { text: string; tone?: string }) {
  return <p className={cn('text-[11.5px] leading-snug sm:pl-[76px]', tone ?? 'text-muted-foreground')}>{text}</p>
}

/**
 * The numbers, each one carrying what it is worth.
 *
 * No figure appears without its sample size, and a rate is only ever shown as the
 * lower bound it is. The qualifier is not a tooltip: a bound presented as a
 * percentage is exactly as misleading whether or not the reader hovers it.
 */
function Evidence({ rows }: { rows: EvidenceRow[] }) {
  return (
    <dl className="grid gap-x-4 gap-y-2 sm:grid-cols-3 sm:pl-[76px]">
      {rows.map((row) => (
        <div key={row.label} className="min-w-0">
          <dt className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">{row.label}</dt>
          <dd>
            <span className={cn('text-[12px] tabular-nums', row.proven ? 'text-foreground' : 'text-muted-foreground')}>
              {row.value}
            </span>
            <span className="mt-0.5 block text-[10.5px] leading-snug text-faint">{row.detail}</span>
          </dd>
        </div>
      ))}
    </dl>
  )
}
