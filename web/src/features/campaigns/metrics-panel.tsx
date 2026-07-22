import { Info } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { SectionBar } from '@/components/layout/page'
import { cn } from '@/lib/utils'
import type { Metrics } from '@/store/api'
import { useUpdateCampaignTrackingMutation } from './api'

/** 0..1 fraction to a one-decimal percentage, e.g. 0.4321 -> "43.2%". */
function formatRate(rate?: number): string {
  return `${((rate ?? 0) * 100).toFixed(1)}%`
}

/**
 * Engagement metrics for a campaign, plus the tracking on/off toggle.
 *
 * Clicks are the headline number — pixel-based open tracking is inflated by
 * mail-client link-prefetch scanners, so opens are shown as "indicative" with
 * a note explaining why, rather than presented as a reliable engagement rate.
 *
 * No loading state of its own — the parent only mounts this once the campaign
 * detail query has resolved (it already shows a skeleton for the Sends grid).
 */
export function MetricsPanel({
  campaignId,
  metrics,
  trackingEnabled,
}: {
  campaignId: string
  metrics?: Metrics
  trackingEnabled?: boolean
}) {
  const sent = metrics?.sent ?? 0
  const hasData = sent > 0
  const rate = (value?: number) => (hasData ? formatRate(value) : 'No data yet')

  return (
    <div className="border-b border-border bg-surface/40">
      <SectionBar label="Engagement">
        <TrackingToggle campaignId={campaignId} enabled={!!trackingEnabled} />
      </SectionBar>

      <div className="grid grid-cols-2 gap-px bg-border md:grid-cols-3">
        <MetricCell label="Sent" value={sent} />
        <MetricCell
          label="Opens"
          badge={<IndicativeBadge />}
          value={metrics?.opens_indicative ?? 0}
          rate={rate(metrics?.open_rate)}
        />
        <MetricCell
          label="Clicks"
          badge={
            <Badge variant="ok" className="rounded-sm px-1.5 py-0 text-[9.5px] normal-case tracking-normal">
              Reliable
            </Badge>
          }
          value={metrics?.clicks ?? 0}
          rate={rate(metrics?.click_rate)}
          emphasize
        />
        <MetricCell label="Replies" value={metrics?.replies ?? 0} rate={rate(metrics?.reply_rate)} />
        <MetricCell label="Bounces" value={metrics?.bounces ?? 0} rate={rate(metrics?.bounce_rate)} />
        <MetricCell label="Unsubscribes" value={metrics?.unsubscribes ?? 0} rate={rate(metrics?.unsub_rate)} />
      </div>
    </div>
  )
}

function MetricCell({
  label,
  value,
  rate,
  badge,
  emphasize,
}: {
  label: string
  value: number
  rate?: string
  badge?: React.ReactNode
  emphasize?: boolean
}) {
  return (
    <div className="bg-surface px-5 py-3.5">
      <div className="flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
        {label}
        {badge}
      </div>
      <div
        className={cn(
          'mt-1 text-[27px] font-light leading-none tabular-nums',
          emphasize ? 'text-ok' : 'text-foreground',
        )}
      >
        {value}
      </div>
      {rate && <div className="mt-1 font-mono text-[11px] text-muted-foreground">{rate}</div>}
    </div>
  )
}

function IndicativeBadge() {
  return (
    // Local provider — this panel doesn't assume it's mounted under app-shell's.
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <Badge
            variant="outline"
            className="cursor-help gap-0.5 rounded-sm px-1.5 py-0 text-[9.5px] normal-case tracking-normal"
          >
            Indicative
            <Info className="size-3" />
          </Badge>
        </TooltipTrigger>
        <TooltipContent className="max-w-64 text-[12px] leading-snug">
          Many mail clients prefetch images to scan for threats, which fires the open pixel without a
          human reading the email. Treat opens as directional — use clicks for a reliable rate.
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

function TrackingToggle({ campaignId, enabled }: { campaignId: string; enabled: boolean }) {
  const [updateTracking, { isLoading }] = useUpdateCampaignTrackingMutation()

  return (
    <label className="flex items-center gap-2 font-mono text-[10.5px] uppercase tracking-[0.14em] text-faint">
      Tracking
      <button
        type="button"
        role="switch"
        aria-checked={enabled}
        aria-label="Toggle open and click tracking"
        disabled={isLoading}
        onClick={() =>
          updateTracking({ id: campaignId, updateCampaignTrackingRequest: { enabled: !enabled } })
        }
        className={cn(
          'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors disabled:opacity-50',
          enabled ? 'bg-ok' : 'bg-border-strong',
        )}
      >
        <span
          className={cn(
            'inline-block size-3.5 rounded-full bg-background shadow transition-transform',
            enabled ? 'translate-x-[18px]' : 'translate-x-1',
          )}
        />
      </button>
    </label>
  )
}
