// Delay helpers for sequence steps. A step's `delay_seconds` is the wait after
// the previous step's send before this one fires. The editor collects the delay
// as whole days + hours; these convert to/from the seconds the API stores and
// render the human label on each card.

const SECONDS_PER_HOUR = 3600
const SECONDS_PER_DAY = 86400

/** Whole days + hours → total seconds. Negatives are clamped to 0. */
export function delayToSeconds(days: number, hours: number): number {
  const d = Number.isFinite(days) && days > 0 ? Math.floor(days) : 0
  const h = Number.isFinite(hours) && hours > 0 ? Math.floor(hours) : 0
  return d * SECONDS_PER_DAY + h * SECONDS_PER_HOUR
}

/** Total seconds → whole days + hours for the edit form's number inputs. */
export function secondsToDelay(seconds: number): { days: number; hours: number } {
  const s = Number.isFinite(seconds) && seconds > 0 ? seconds : 0
  return {
    days: Math.floor(s / SECONDS_PER_DAY),
    hours: Math.floor((s % SECONDS_PER_DAY) / SECONDS_PER_HOUR),
  }
}

/**
 * Human label for a step card: `0`/absent → "Immediately", otherwise the
 * coarsest useful breakdown ("3 days after previous", "5 hours after previous",
 * "1 day 2 hours after previous"). Minutes only surface below an hour so a
 * back-end-set sub-hour delay still reads sensibly.
 */
export function humanizeDelay(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) return 'Immediately'
  const days = Math.floor(seconds / SECONDS_PER_DAY)
  const hours = Math.floor((seconds % SECONDS_PER_DAY) / SECONDS_PER_HOUR)
  const minutes = Math.floor((seconds % SECONDS_PER_HOUR) / 60)

  const parts: string[] = []
  if (days > 0) parts.push(`${days} day${days === 1 ? '' : 's'}`)
  if (hours > 0) parts.push(`${hours} hour${hours === 1 ? '' : 's'}`)
  if (days === 0 && hours === 0 && minutes > 0) parts.push(`${minutes} minute${minutes === 1 ? '' : 's'}`)

  if (parts.length === 0) return 'Immediately'
  return `${parts.join(' ')} after previous`
}
