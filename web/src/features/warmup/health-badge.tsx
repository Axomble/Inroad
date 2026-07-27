import { cn } from '@/lib/utils'
import { healthMeta, toWarmupHealth } from './health'

/**
 * A mailbox's warmup reputation state as a small pill. Color is reinforcement
 * only — the uppercase text label is the signal, so the four states stay
 * tellable apart for colorblind users and in both themes. An optional
 * `reason` (the backend's `health_reason`) is exposed as a tooltip/title for a
 * non-healthy state without cluttering the badge.
 */
export function HealthBadge({
  state,
  reason,
  className,
}: {
  state: string
  reason?: string
  className?: string
}) {
  const key = toWarmupHealth(state)
  const { label, text, dot, bg } = healthMeta[key]
  return (
    <span
      data-slot="health-badge"
      title={reason || undefined}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2 py-0.5',
        'font-mono text-[10.5px] font-medium uppercase tracking-[0.1em]',
        bg,
        text,
        className,
      )}
    >
      <span className={cn('size-1.5 rounded-full', dot)} aria-hidden="true" />
      {label}
    </span>
  )
}
