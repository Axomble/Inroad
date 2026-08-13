import type { WarmupMailbox } from '@/store/api'

/**
 * The pool-eligibility lanes the backend assigns a warming mailbox
 * (`warmup_participants.lane`), taken from the generated API type so this stays
 * in lockstep with the contract — add a lane there and the `laneMeta` map below
 * fails to compile until it covers it.
 *
 * A lane is NOT a reputation verdict. `WarmupHealth` (`warmup-health.ts`)
 * answers "how does this mailbox's outbound mail perform"; a lane answers "which
 * peers may it exchange warmup mail with, and may it take new campaign leads".
 * The two axes are independent and unordered against each other: a mailbox can
 * be reputation-healthy while still `probation` because it has not proven itself
 * yet, or `quarantine` while its last measured reputation was fine.
 */
export type WarmupLane = WarmupMailbox['lane']

/**
 * Per-lane label + palette, deliberately the same shape as `healthMeta` so both
 * axes share one mental model. The label is the primary signal (color is
 * redundant reinforcement, colorblind-safe), which is why five severity tones
 * cover seven lanes: the tone says how serious, the label says which lane.
 *
 * Tones come from the shared "Volt" tokens and are dark-mode aware:
 * pending_auth → muted (nothing is wrong; DNS just hasn't answered),
 * probation/recovery → warm (the reserved warmup "heat" hue — working, unproven),
 * healthy → ok, watch → warn, quarantine/blocked → danger (withheld from the
 * pool and barred from new campaign leads, the hardest facts on this axis).
 *
 * The labels are scoped by the axis word `LaneBadge` renders alongside them
 * ("Pool · Proving"), so `healthy` reads "Healthy" here and is never presented
 * bare next to a reputation badge.
 */
export const laneMeta: Record<
  WarmupLane,
  { label: string; text: string; dot: string; bg: string }
> = {
  pending_auth: { label: 'Awaiting DNS', text: 'text-muted-foreground', dot: 'bg-muted-foreground', bg: 'bg-surface-2' },
  probation: { label: 'Proving', text: 'text-warm', dot: 'bg-warm', bg: 'bg-warm/12' },
  healthy: { label: 'Healthy', text: 'text-ok', dot: 'bg-ok', bg: 'bg-ok/12' },
  watch: { label: 'Watch', text: 'text-warn', dot: 'bg-warn', bg: 'bg-warn/12' },
  recovery: { label: 'Recovering', text: 'text-warm', dot: 'bg-warm', bg: 'bg-warm/12' },
  quarantine: { label: 'Withheld', text: 'text-danger', dot: 'bg-danger', bg: 'bg-danger/12' },
  blocked: { label: 'Blocked', text: 'text-danger', dot: 'bg-danger', bg: 'bg-danger/12' },
}

/**
 * Narrow an arbitrary backend string to a known lane, falling back to
 * `probation` — the unproven lane — for anything absent or unrecognised.
 *
 * The fallback direction is the safety property: a missing or newer-than-this-
 * build lane must never render as "in the healthy pool", because that is the
 * claim an operator acts on when deciding a mailbox is safe to send from. This
 * mirrors `toWarmupHealth`, which falls back to `unknown` rather than `healthy`
 * for the same reason. (The generated type is a closed union, but the JSON
 * boundary is untyped, so we validate what actually crossed it.)
 */
export function toWarmupLane(value: string | null | undefined): WarmupLane {
  return value != null && value in laneMeta ? (value as WarmupLane) : 'probation'
}
