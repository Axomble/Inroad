import { cn } from '@/lib/utils'
import { laneMeta, toWarmupLane } from '@/lib/warmup-lane'

/**
 * A mailbox's warmup **pool eligibility** as a small chip.
 *
 * Deliberately not shaped like `HealthBadge`: reputation is an attribute of the
 * mailbox (a round, filled pill with a dot), while a lane is the group the
 * mailbox is placed in — so this is a squared chip with a leading rule and the
 * axis word "Pool" spelled out. An operator seeing `HEALTHY` next to
 * `POOL PROVING` cannot read the two as one status, and the axis is named in
 * text rather than implied by color or position.
 *
 * Color is reinforcement only; the uppercase label is the signal. The lane's
 * explanation (`lane_reason`) is intentionally not rendered here — callers with
 * room show it as their own line rather than hiding it in a `title`.
 */
export function LaneBadge({ lane, className }: { lane: string; className?: string }) {
  const { label, text, dot, bg } = laneMeta[toWarmupLane(lane)]
  return (
    <span
      data-slot="lane-badge"
      className={cn(
        'inline-flex items-center gap-1.5 rounded-[3px] px-1.5 py-0.5',
        'font-mono text-[10.5px] font-medium uppercase tracking-[0.1em]',
        bg,
        text,
        className,
      )}
    >
      {/* A rule, not a dot — the other half of the shape contrast with HealthBadge. */}
      <span className={cn('h-2.5 w-[2px] rounded-[1px]', dot)} aria-hidden="true" />
      <span className="opacity-60">Pool</span>
      {/* Flex `gap` is visual only, so an explicit space keeps the chip from
          reading as one word ("PoolWithheld") to a screen reader. A whitespace-
          only run between flex items is not rendered, so the layout is unchanged. */}
      {' '}
      <span>{label}</span>
    </span>
  )
}
