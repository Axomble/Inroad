import { StatusDot, type StatusTone } from '@/components/shared/status-pill'
import { relativeTime } from '@/lib/relative-time'
import { cn } from '@/lib/utils'
import type { FleetProviderSignals, FleetWorker } from './api'
import {
  formatCount,
  formatPercent,
  idFamilyDetail,
  livenessCopy,
  operationLabel,
  percentOf,
  providerLabel,
  signalSeverity,
  signalSummary,
  type SignalSeverity,
} from './fleet-copy'

/**
 * One worker, and what its providers have been saying.
 *
 * The layout is built around one rule: an operator must be able to see a
 * degrading worker WITHOUT DOING ARITHMETIC. So every number that only means
 * something in relation to another is rendered beside it —
 *
 *   attempts with successes, because a success count alone cannot tell a
 *   healthy worker from an idle one;
 *   auth failures with throttles, because together they are the picture of a
 *   provider losing patience with an address;
 *   mailboxes with degraded mailboxes, because three of four is a different
 *   worker from three of three hundred.
 *
 * Percentages sit BESIDE the counts they were derived from, never instead of
 * them: "4 of 400 (1%)" is actionable and "1%" is not, because the reader
 * cannot tell whether it came from a busy worker or from four attempts total.
 */
export function FleetWorkerRow({ worker }: { worker: FleetWorker }) {
  const liveness = livenessCopy(worker.live)
  const degradedShare = percentOf(worker.degraded_mailbox_count, worker.mailbox_count)

  return (
    <li data-slot="fleet-worker" className="border-b border-border px-4 py-3 sm:px-5">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="min-w-0">
          <p className="flex flex-wrap items-center gap-2">
            {/*
              The egress IP leads, not the worker id. The address is the thing a
              provider forms an opinion about and the thing an operator can look
              up in a provider's own security log; the id is how we refer to the
              machine internally and is only useful for matching against the
              decision log below.
            */}
            <span data-slot="fleet-worker-ip" className="truncate font-mono text-[12.5px] text-foreground">
              {worker.egress_ip === '' ? 'address unknown' : worker.egress_ip}
            </span>
            <span
              data-slot="fleet-worker-liveness"
              title={liveness.detail}
              className="inline-flex shrink-0 items-center gap-1.5"
            >
              <StatusDot tone={worker.live ? 'running' : 'failing'} />
              <span
                className={cn(
                  'font-mono text-[9.5px] uppercase tracking-[0.12em]',
                  worker.live ? 'text-ok' : 'text-danger',
                )}
              >
                {liveness.label}
              </span>
            </span>
          </p>
          <p data-slot="fleet-worker-meta" className="mt-0.5 text-[12px] leading-snug text-muted-foreground">
            <span title={idFamilyDetail(worker.id_family)} className="font-mono">
              {worker.worker_id}
            </span>
            {' · last heartbeat '}
            {relativeTime(worker.last_seen_at)}
            {' · carrying your mail since '}
            {relativeTime(worker.first_assigned_at)}
          </p>
        </div>

        <div data-slot="fleet-worker-mailboxes" className="shrink-0 text-right">
          <p className="font-mono text-[12.5px] tabular-nums text-foreground">
            {formatCount(worker.mailbox_count)}
            <span className="text-faint"> of your mailboxes</span>
          </p>
          <p className="mt-0.5 font-mono text-[11px] tabular-nums text-muted-foreground">
            {worker.degraded_mailbox_count === 0 ? (
              'none degraded'
            ) : (
              <span className="text-warn">
                {formatCount(worker.degraded_mailbox_count)} degraded ({formatPercent(degradedShare)})
              </span>
            )}
          </p>
        </div>
      </div>

      {worker.signals.length === 0 ? (
        /*
          Silence, said as silence. A row of zeroes here would read as "400
          attempts, 0 successes" — the worst possible worker — when it means the
          worker reported nothing at all, which is usually just an idle one.
        */
        <p data-slot="fleet-worker-quiet" className="mt-2 text-[12px] leading-snug text-faint">
          No provider reported anything about this address in this window. That is silence, not success — an idle
          worker and one whose flushes are not arriving look the same from here.
        </p>
      ) : (
        <ul data-slot="fleet-worker-signals" className="mt-2 space-y-1.5">
          {worker.signals.map((signal) => (
            <SignalLine key={`${signal.provider}:${signal.operation}`} signal={signal} />
          ))}
        </ul>
      )}
    </li>
  )
}

const SEVERITY_TONE: Record<SignalSeverity, StatusTone> = {
  quiet: 'draft',
  healthy: 'running',
  watch: 'warming',
  bad: 'failing',
}

/**
 * One (provider, operation) leg.
 *
 * AUTH FAILURES ARE FIRST-CLASS HERE and are not in a tooltip. They are the
 * provider-side signal that predicts an address being challenged, which is the
 * reason these counters are collected; a screen that made an operator hover to
 * find them would have defeated the collection.
 *
 * `rejected` is rendered last and visually apart, because it is the one number
 * on this line that is NOT about the worker — a permanent rejection is about
 * the recipient, and an operator who reads it as worker health will move
 * mailboxes for no reason.
 */
function SignalLine({ signal }: { signal: FleetProviderSignals }) {
  const severity = signalSeverity(signal)
  const successRate = percentOf(signal.successes, signal.attempts)
  const authRate = percentOf(signal.auth_failures, signal.attempts)

  return (
    <li data-slot="fleet-signal" className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-[12px] leading-snug">
      <span className="inline-flex shrink-0 items-center gap-1.5" title={signalSummary(signal)}>
        <StatusDot tone={SEVERITY_TONE[severity]} />
        <span className="font-mono text-[11px] text-muted-foreground">
          {providerLabel(signal.provider)} {operationLabel(signal.operation)}
        </span>
      </span>

      {/* Colour is never the only signal: every figure carries its own word. */}
      <span data-slot="fleet-signal-attempts" className="font-mono tabular-nums text-muted-foreground">
        {formatCount(signal.successes)}/{formatCount(signal.attempts)} accepted ({formatPercent(successRate)})
      </span>

      <span
        data-slot="fleet-signal-auth"
        className={cn('font-mono tabular-nums', signal.auth_failures > 0 ? 'text-danger' : 'text-faint')}
      >
        {formatCount(signal.auth_failures)} credential refused ({formatPercent(authRate)})
      </span>

      <span
        data-slot="fleet-signal-throttled"
        className={cn('font-mono tabular-nums', signal.throttled > 0 ? 'text-warn' : 'text-faint')}
      >
        {formatCount(signal.throttled)} throttled
      </span>

      {signal.blocked > 0 && (
        <span data-slot="fleet-signal-blocked" className="font-mono tabular-nums text-danger">
          {formatCount(signal.blocked)} address refused
        </span>
      )}

      {signal.rejected > 0 && (
        <span
          data-slot="fleet-signal-rejected"
          title="About the recipient — a dead address or an oversized message — not about this worker."
          className="font-mono tabular-nums text-faint"
        >
          · {formatCount(signal.rejected)} rejected by recipient
        </span>
      )}
    </li>
  )
}
