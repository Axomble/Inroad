// Minute-of-day ↔ "HH:MM" conversion for the send-window editor, plus the
// weekday labels. Component-free so the editor and its tests share one
// implementation of the parsing rules rather than each restating them.

/** Weekday labels indexed to match the API's `weekday` (0 = Sunday). */
export const WEEKDAY_LABELS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'] as const

/** Short labels for the compact per-day rows. */
export const WEEKDAY_SHORT = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'] as const

export const MINUTES_PER_DAY = 24 * 60

/**
 * Minutes from midnight to the "HH:MM" value an `<input type="time">` expects.
 * 1440 (exclusive midnight, the API's maximum end) renders as "24:00", which no
 * time input accepts, so it clamps to "23:59" — the closest representable value.
 */
export function minutesToTime(minutes: number): string {
  const clamped = Math.max(0, Math.min(minutes, MINUTES_PER_DAY - 1))
  const h = Math.floor(clamped / 60)
  const m = clamped % 60
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`
}

/**
 * "HH:MM" back to minutes from midnight. Returns null for anything unparseable
 * or out of range, so a half-typed value can't be silently coerced to 00:00 and
 * saved as a window starting at midnight.
 */
export function timeToMinutes(value: string): number | null {
  const match = /^(\d{1,2}):(\d{2})$/.exec(value.trim())
  if (!match) return null
  const h = Number(match[1])
  const m = Number(match[2])
  if (!Number.isInteger(h) || !Number.isInteger(m)) return null
  if (h < 0 || h > 23 || m < 0 || m > 59) return null
  return h * 60 + m
}

/** Human range for a read-only summary, e.g. "09:00 – 17:00". */
export function formatRange(startMinute: number, endMinute: number): string {
  return `${minutesToTime(startMinute)} – ${minutesToTime(endMinute)}`
}
