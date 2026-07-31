import type { WarmupMailbox } from '@/store/api'

/**
 * The reputation states the backend derives for a warming mailbox
 * (`warmup_participants.health_state`), taken from the generated API type so
 * this stays in lockstep with the contract — add a state there and the
 * `healthMeta` map below fails to compile until it covers it.
 */
export type WarmupHealth = WarmupMailbox['health_state']

/**
 * Per-state label + palette. The label is the primary signal so state reads
 * without relying on color (colorblind-safe); the four tones are distinct and
 * dark-mode aware because they come from the shared "Volt" tokens:
 * healthy → ok (green), watch → warn (amber), throttled → warm (orange, the
 * reserved warmup "heat" hue), paused → danger (red).
 */
export const healthMeta: Record<
  WarmupHealth,
  { label: string; text: string; dot: string; bg: string }
> = {
  healthy: { label: 'Healthy', text: 'text-ok', dot: 'bg-ok', bg: 'bg-ok/12' },
  watch: { label: 'Watch', text: 'text-warn', dot: 'bg-warn', bg: 'bg-warn/12' },
  throttled: { label: 'Throttled', text: 'text-warm', dot: 'bg-warm', bg: 'bg-warm/12' },
  paused: { label: 'Paused', text: 'text-danger', dot: 'bg-danger', bg: 'bg-danger/12' },
}

/**
 * Narrow an arbitrary backend string to a known WarmupHealth; anything
 * unexpected falls back to `healthy` so a card never renders a blank badge.
 * (The generated type is already a closed union, but the JSON boundary is
 * untyped, so we validate what actually crossed it.)
 */
export function toWarmupHealth(value: string | null | undefined): WarmupHealth {
  return value != null && value in healthMeta ? (value as WarmupHealth) : 'healthy'
}
