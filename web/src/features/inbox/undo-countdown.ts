// The undo window's countdown, as pure functions so the boundary behaviour is
// unit-testable rather than only observable through a ticking component.

/** Whole seconds remaining until `sendAfter`, floored at 0. */
export function secondsUntil(sendAfter: string, now: Date): number {
  const at = new Date(sendAfter)
  if (Number.isNaN(at.getTime())) return 0
  return Math.max(0, Math.ceil((at.getTime() - now.getTime()) / 1000))
}

/**
 * Whether a countdown is worth showing at all.
 *
 * A reply already due (or one scheduled days out) gets no ticking timer: the
 * first has nothing left to count, and the second would show a meaningless
 * four-digit number. `COUNTDOWN_CEILING_SECONDS` is the line between "undo
 * this" and "scheduled for later", which the outbox presents as a date instead.
 */
export const COUNTDOWN_CEILING_SECONDS = 120

export function showsCountdown(sendAfter: string, now: Date): boolean {
  const remaining = secondsUntil(sendAfter, now)
  return remaining > 0 && remaining <= COUNTDOWN_CEILING_SECONDS
}

/**
 * How a queued reply's timing reads: a live countdown inside the undo window,
 * otherwise the absolute moment it will leave.
 *
 * `now` is a parameter, not read from the clock, so a rendered list measures
 * every row against one instant and the rules are testable at a fixed one.
 */
export function sendTimingLabel(sendAfter: string, now: Date): string {
  if (showsCountdown(sendAfter, now)) {
    const remaining = secondsUntil(sendAfter, now)
    return `Sending in ${remaining}s`
  }
  const at = new Date(sendAfter)
  if (Number.isNaN(at.getTime())) return 'Sending soon'
  if (at <= now) return 'Sending now'
  return `Scheduled for ${at.toLocaleString(undefined, {
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
  })}`
}

/**
 * The statuses a queued reply can be in, as the API reports them. Mirrors the
 * server's own enum; kept local so a status the API drops fails `tsc` here.
 */
export type PendingStatus = 'scheduled' | 'sending' | 'sent' | 'cancelled' | 'failed'

/** Human copy for a queued reply's status, for the outbox. */
export const PENDING_STATUS_LABELS: Record<PendingStatus, string> = {
  scheduled: 'Queued',
  sending: 'Sending',
  sent: 'Sent',
  cancelled: 'Cancelled',
  failed: 'Failed',
}
