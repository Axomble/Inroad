import { StatusPill, type StatusTone } from '@/components/shared/status-pill'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import type { ConnectionStatus, RealtimeState } from './socket-events'
import { useRealtimeStatus } from './realtime-context'

/**
 * Required, not decoration (spec §6): a silently dead socket looks exactly like
 * a quiet workspace, which is the failure mode the DSN bounce fix existed to
 * eliminate. It renders nothing while healthy — an always-visible green light is
 * chrome nobody reads — and speaks up the moment the connection is not live.
 *
 * Color is never the only signal: every state carries an uppercase text label
 * beside the dot, and the tooltip says what the user can do about it.
 */
const presentation: Record<
  Exclude<ConnectionStatus, 'live' | 'idle'>,
  { tone: StatusTone; label: string; detail: string }
> = {
  connecting: {
    tone: 'draft',
    label: 'Connecting',
    detail: 'Opening the live connection to this workspace.',
  },
  reconnecting: {
    tone: 'paused',
    label: 'Reconnecting',
    detail: 'The live connection dropped. Retrying — recent changes may take a moment to appear.',
  },
  offline: {
    tone: 'failing',
    label: 'Offline',
    detail: 'Live updates have stopped. Reload the page to reconnect.',
  },
}

export function ConnectionIndicatorView({ status, error, gapDetected }: RealtimeState) {
  if (status === 'idle') return null
  if (status === 'live') {
    if (!gapDetected) return null
    // Live, but a replay window was missed: patched caches are known-incomplete,
    // which is worth saying rather than showing quietly stale numbers.
    return (
      <Indicator
        tone="paused"
        label="Catching up"
        detail="Some updates were missed while offline. Reload the page for a complete view."
      />
    )
  }
  const { tone, label, detail } = presentation[status]
  return <Indicator tone={tone} label={label} detail={error ?? detail} />
}

function Indicator({ tone, label, detail }: { tone: StatusTone; label: string; detail: string }) {
  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            // Not an action — a status affordance whose only job is to reveal the
            // detail on hover and focus. `tabIndex` keeps it keyboard-reachable
            // so the explanation is not hover-only.
            aria-label={`Live updates: ${label}. ${detail}`}
            className="rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <StatusPill tone={tone}>{label}</StatusPill>
          </button>
        </TooltipTrigger>
        <TooltipContent>{detail}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

/** The mounted form: reads the provider's context. */
export function ConnectionIndicator() {
  return <ConnectionIndicatorView {...useRealtimeStatus()} />
}
