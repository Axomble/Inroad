import { Link } from '@tanstack/react-router'
import { Flame, Reply } from 'lucide-react'
import { formatClock24 } from '@/lib/datetime'
import { cn } from '@/lib/utils'
import { Skeleton } from '@/components/ui/skeleton'
import type { PulseAttentionItem, PulseSeverity, WorkspacePulse } from '@/features/pulse/api'
import { SEVERITY_SR, attentionLabel, linkProps, sortAttention } from './pulse-attention'
import { usePulse } from './use-pulse'

/**
 * The chrome's answer to "is everything okay?" — sits above the nav in the
 * sidebar (spec: docs/superpowers/specs/2026-08-04-console-redesign.md §1).
 *
 * Two postures, deliberately asymmetric:
 * - **Quiet when healthy.** Zero attention rows collapse the card to two muted
 *   lines; its size and color are themselves the signal.
 * - **Worst-first when not.** Attention rows are server-defined
 *   (`pulse.attention[]`) and sort danger > warn > info, so the top line of
 *   the sidebar is always the most important fact in the workspace.
 *
 * Risk is only half the question. Attention rows, the send meter and the
 * warmup line all answer "am I safe?"; the replies line answers "is it
 * working?" — the outcome the sending exists to produce. It renders in both
 * postures because a bad day for deliverability and a good day for replies are
 * independent facts, and suppressing the second while showing the first would
 * make the card read as worse news than the workspace is actually having.
 *
 * Conditional lines earn their space: replies appear only with unread threads,
 * warmup only with a non-empty pool. Nothing here renders a zero.
 *
 * Accent discipline: the send meter's `--primary` fill is the only lime; the
 * warmup line's `--warm` is the only orange; attention rows and the interested
 * count use the semantic ok/warn/danger scale. Everything else is `--chrome-*`.
 */

// Shape varies with color so severity survives colorblindness; each glyph is
// aria-hidden and paired with a visually-hidden severity word.
const SEVERITY_GLYPH: Record<PulseSeverity, string> = { danger: '▲', warn: '◆', info: '●' }
const SEVERITY_TEXT: Record<PulseSeverity, string> = { danger: 'text-danger', warn: 'text-warn', info: 'text-ok' }

const rowClass =
  '-mx-1 flex items-center gap-2 rounded-md px-1 py-0.5 outline-none transition-colors hover:bg-chrome-hover focus-visible:ring-2 focus-visible:ring-primary'

function AttentionRow({ item }: { item: PulseAttentionItem }) {
  return (
    <Link {...linkProps(item.href)} data-slot="pulse-attention-row" className={cn(rowClass, 'text-[12px] text-chrome-text')}>
      <span className={cn('shrink-0 font-mono text-[10px] leading-none', SEVERITY_TEXT[item.severity])} aria-hidden="true">
        {SEVERITY_GLYPH[item.severity]}
      </span>
      <span className="sr-only">{SEVERITY_SR[item.severity]}</span>
      <span className="truncate">
        <span className="font-mono tabular-nums">{item.count}</span> {attentionLabel(item.kind, item.count)}
      </span>
      <span className="ml-auto shrink-0 font-mono text-[10px] text-chrome-muted">{item.reason}</span>
    </Link>
  )
}

function SendMeter({ sending }: { sending: WorkspacePulse['sending'] }) {
  const capped = sending.daily_cap > 0
  const pct = capped ? Math.min(100, (sending.sent_today / sending.daily_cap) * 100) : 0
  return (
    <Link to="/app/campaigns" data-slot="pulse-send-meter" className={cn(rowClass, 'flex-col items-stretch gap-1')}>
      {capped && (
        <span className="h-1 w-full overflow-hidden rounded-full bg-chrome-surface" aria-hidden="true">
          <span className="block h-full rounded-full bg-primary" style={{ width: `${pct}%` }} />
        </span>
      )}
      <span className="text-[12px] text-chrome-muted">
        Sending <span className="font-mono tabular-nums text-chrome-text">{sending.sent_today}</span>
        {capped && (
          <>
            {' / '}
            <span className="font-mono tabular-nums">{sending.daily_cap}</span>
          </>
        )}{' '}
        today
      </span>
    </Link>
  )
}

/**
 * Warmup is two independent axes — pool eligibility (`lane`) and sender
 * reputation (`health_state`) — condensed to one line, worst-first. The order
 * encodes consequence, not the order the payload lists the fields:
 * `quarantine` (withheld) outranks the reputation buckets because a withheld
 * mailbox exchanges no warmup mail *and* takes no new campaign leads, where
 * `at_risk` only reduces volume. `probation` sits last because it is the normal
 * lifecycle of a new mailbox — but it still outranks "all healthy", which would
 * otherwise claim a pool of entirely unproven mailboxes is healthy.
 */
const WARMUP_STATUS_RULES: Array<{ count: (w: WorkspacePulse['warmup']) => number; label: (n: number) => string }> = [
  { count: (w) => w.quarantine, label: (n) => `${n} withheld` },
  { count: (w) => w.at_risk, label: (n) => `${n} at risk` },
  { count: (w) => w.watch, label: (n) => `${n} on watch` },
  { count: (w) => w.unknown, label: (n) => `${n} need evidence` },
  { count: (w) => w.probation, label: (n) => `${n} proving` },
]

function warmupStatus(warmup: WorkspacePulse['warmup']): string {
  for (const rule of WARMUP_STATUS_RULES) {
    const count = rule.count(warmup)
    if (count > 0) return rule.label(count)
  }
  return 'all healthy'
}

/**
 * The one outcome line: replies waiting, and how many of them classified
 * positive. Both numbers are unread-scoped — `interested` is a subset of
 * `unread`, not a lifetime total — so the line reads as a to-do ("6 of the 23
 * waiting are interested") rather than a vanity metric. `text-ok` on the
 * interested count is the semantic scale, not a fourth accent.
 */
function RepliesLine({ inbox }: { inbox: WorkspacePulse['inbox'] }) {
  return (
    <Link to="/app/inbox" data-slot="pulse-replies-line" className={cn(rowClass, 'text-[12px] text-chrome-muted')}>
      <Reply className="size-3 shrink-0" strokeWidth={1.75} aria-hidden="true" />
      <span className="truncate">
        <span className="font-mono tabular-nums text-chrome-text">{inbox.unread}</span>{' '}
        {inbox.unread === 1 ? 'reply' : 'replies'}
        {inbox.interested > 0 && (
          <>
            {' · '}
            <span className="font-mono tabular-nums text-ok">{inbox.interested}</span>
            <span className="text-ok"> interested</span>
          </>
        )}
      </span>
    </Link>
  )
}

function WarmupLine({ warmup }: { warmup: WorkspacePulse['warmup'] }) {
  const status = warmupStatus(warmup)
  return (
    <Link to="/app/warmup" data-slot="pulse-warmup-line" className={cn(rowClass, 'text-[12px] text-chrome-muted')}>
      <Flame className="size-3 shrink-0 text-warm" strokeWidth={1.75} aria-hidden="true" />
      <span className="truncate">
        <span className="font-mono tabular-nums text-chrome-text">{warmup.pool}</span> warming · {status}
      </span>
    </Link>
  )
}

export function PulseCard() {
  const { data, isError, fulfilledTimeStamp } = usePulse()

  const attention = data ? sortAttention(data.attention) : []

  return (
    <section data-slot="pulse-card" aria-label="Workspace pulse" className="mb-5 flex flex-col gap-1.5 border-b border-chrome-border px-2.5 pb-4">
      <div className="flex items-center">
        <span className="font-mono text-[9px] font-medium uppercase tracking-[0.18em] text-chrome-muted/70">Pulse</span>
        {/* Freshness tick — the query's fulfilledTimeStamp, an honest "last
            successful fetch", not a fake latency stat. */}
        <span className="ml-auto flex items-center gap-1 font-mono text-[10px] tabular-nums text-chrome-muted">
          <span className={cn('size-1.5 rounded-full', isError ? 'bg-danger' : 'bg-ok')} aria-hidden="true" />
          <span className="sr-only">{isError ? 'Last successful update' : 'Updated'}</span>
          {/* 24-hour: this card has no room for an AM/PM suffix. */}
          {fulfilledTimeStamp !== undefined ? formatClock24(fulfilledTimeStamp) : '—'}
        </span>
      </div>

      {isError ? (
        <p className="text-[12px] text-danger">Can't reach the server · retrying</p>
      ) : !data ? (
        // Reserved-height skeleton matching the healthy two-line form, so the
        // sidebar never jumps when the first payload lands.
        <div className="flex flex-col gap-1.5" aria-hidden="true">
          <Skeleton className="h-4 w-3/4 bg-chrome-surface" />
          <Skeleton className="h-4 w-1/2 bg-chrome-surface" />
        </div>
      ) : attention.length === 0 ? (
        <>
          <p className="flex items-center gap-2 px-1 py-0.5 text-[12px] text-chrome-muted">
            <span className="size-1.5 shrink-0 rounded-full bg-ok" aria-hidden="true" />
            All systems healthy
          </p>
          <SendMeter sending={data.sending} />
          {data.inbox.unread > 0 && <RepliesLine inbox={data.inbox} />}
        </>
      ) : (
        <>
          {attention.map((item) => (
            <AttentionRow key={item.kind} item={item} />
          ))}
          <SendMeter sending={data.sending} />
          {data.inbox.unread > 0 && <RepliesLine inbox={data.inbox} />}
          {data.warmup.pool > 0 && <WarmupLine warmup={data.warmup} />}
        </>
      )}
    </section>
  )
}
