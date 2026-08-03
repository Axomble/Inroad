import { Link } from '@tanstack/react-router'
import { Flame } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Skeleton } from '@/components/ui/skeleton'
import type { PulseAttentionItem, PulseSeverity, WorkspacePulse } from '@/features/pulse/api'
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
 * Accent discipline: the send meter's `--primary` fill is the only lime; the
 * warmup line's `--warm` is the only orange; attention rows use the semantic
 * ok/warn/danger scale. Everything else is `--chrome-*`.
 */

const SEVERITY_ORDER: Record<PulseSeverity, number> = { danger: 0, warn: 1, info: 2 }
// Shape varies with color so severity survives colorblindness; each glyph is
// aria-hidden and paired with a visually-hidden severity word.
const SEVERITY_GLYPH: Record<PulseSeverity, string> = { danger: '▲', warn: '◆', info: '●' }
const SEVERITY_TEXT: Record<PulseSeverity, string> = { danger: 'text-danger', warn: 'text-warn', info: 'text-ok' }
const SEVERITY_SR: Record<PulseSeverity, string> = { danger: 'Critical:', warn: 'Warning:', info: 'Notice:' }

/**
 * Copy for known attention kinds; unknown kinds (a newer server) fall back to
 * the humanized identifier so new producers render without a frontend change.
 */
// Keys mirror the server's kind constants (internal/app/pulse/service.go);
// pulse-card.test.tsx asserts every known kind maps here so a renamed
// producer can't silently fall through to the humanized fallback again.
const ATTENTION_LABELS: Record<string, (count: number) => string> = {
  mailbox_error: (n) => (n === 1 ? 'mailbox needs attention' : 'mailboxes need attention'),
  senders_gated: (n) => (n === 1 ? 'sender gated' : 'senders gated'),
  dmarc_failing: (n) => (n === 1 ? 'domain failing DMARC' : 'domains failing DMARC'),
  cap_consumed: (n) => (n === 1 ? 'sending pool near daily cap' : 'sending pools near daily cap'),
}

function attentionLabel(kind: string, count: number): string {
  const label = ATTENTION_LABELS[kind]
  return label ? label(count) : kind.replace(/_/g, ' ')
}

/**
 * Server hrefs may carry a query string (`/app/mailboxes?status=error`);
 * TanStack's Link wants the search separated from the path.
 */
function linkProps(href: string): { to: string; search?: Record<string, string> } {
  const queryStart = href.indexOf('?')
  if (queryStart === -1) return { to: href }
  return {
    to: href.slice(0, queryStart),
    search: Object.fromEntries(new URLSearchParams(href.slice(queryStart + 1))),
  }
}

function formatTick(timestamp: number): string {
  return new Date(timestamp).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })
}

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

function WarmupLine({ warmup }: { warmup: WorkspacePulse['warmup'] }) {
  const status =
    warmup.at_risk > 0 ? `${warmup.at_risk} at risk` : warmup.watch > 0 ? `${warmup.watch} on watch` : 'all healthy'
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

  const attention = data ? [...data.attention].sort((a, b) => SEVERITY_ORDER[a.severity] - SEVERITY_ORDER[b.severity]) : []

  return (
    <section data-slot="pulse-card" aria-label="Workspace pulse" className="mb-5 flex flex-col gap-1.5 border-b border-chrome-border px-2.5 pb-4">
      <div className="flex items-center">
        <span className="font-mono text-[9px] font-medium uppercase tracking-[0.18em] text-chrome-muted/70">Pulse</span>
        {/* Freshness tick — the query's fulfilledTimeStamp, an honest "last
            successful fetch", not a fake latency stat. */}
        <span className="ml-auto flex items-center gap-1 font-mono text-[10px] tabular-nums text-chrome-muted">
          <span className={cn('size-1.5 rounded-full', isError ? 'bg-danger' : 'bg-ok')} aria-hidden="true" />
          <span className="sr-only">{isError ? 'Last successful update' : 'Updated'}</span>
          {fulfilledTimeStamp !== undefined ? formatTick(fulfilledTimeStamp) : '—'}
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
        </>
      ) : (
        <>
          {attention.map((item) => (
            <AttentionRow key={item.kind} item={item} />
          ))}
          <SendMeter sending={data.sending} />
          {data.warmup.pool > 0 && <WarmupLine warmup={data.warmup} />}
        </>
      )}
    </section>
  )
}
