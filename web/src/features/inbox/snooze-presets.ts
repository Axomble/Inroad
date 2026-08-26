// The snooze menu's presets, resolved to absolute instants.
//
// Presets live on the CLIENT, not the server: "tomorrow morning" means 9am in
// the viewer's own timezone, and only the browser knows that. The API takes an
// absolute RFC3339 instant and its job is to bound and store it (see
// snoozeInboxThread's description) — so this module is where "later today"
// becomes a timestamp, and it is pure so those rules are unit-testable at a
// fixed instant.

/** The hour "morning" presets land on, in the viewer's local time. */
const MORNING_HOUR = 9

/** Mirrors the API's own bound (SnoozeMaxHorizon in the Go domain). */
export const SNOOZE_MAX_DAYS = 90

export interface SnoozePreset {
  /** Stable key, for React keys and tests. */
  id: string
  label: string
  /** The instant to snooze until, or null when the preset is not offerable now. */
  resolve: (now: Date) => Date | null
}

function atHour(day: Date, hour: number): Date {
  return new Date(day.getFullYear(), day.getMonth(), day.getDate(), hour, 0, 0, 0)
}

function addDays(d: Date, days: number): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + days, d.getHours(), d.getMinutes(), 0, 0)
}

/** Days until the next Monday. Always 1..7, never 0 — "next week" is never today. */
function daysUntilNextMonday(now: Date): number {
  const MONDAY = 1
  return ((MONDAY - now.getDay() + 7) % 7) || 7
}

/**
 * The presets, in menu order.
 *
 * Each returns null when it would resolve to a moment already past — "Later
 * today" is meaningless at 11pm, and offering it would produce a 422 from the
 * API. The menu omits those rather than showing a control that cannot work.
 */
export const SNOOZE_PRESETS: readonly SnoozePreset[] = [
  {
    id: 'later_today',
    label: 'Later today',
    // Three hours on, rounded to the hour so the menu doesn't promise
    // "14:37". Null once that would spill past ~10pm, at which point "later
    // today" is really tomorrow and Tomorrow says so honestly.
    resolve: (now) => {
      const at = new Date(now.getFullYear(), now.getMonth(), now.getDate(), now.getHours() + 3, 0, 0, 0)
      return at.getDate() === now.getDate() && at.getHours() <= 22 ? at : null
    },
  },
  {
    id: 'tomorrow',
    label: 'Tomorrow',
    resolve: (now) => atHour(addDays(now, 1), MORNING_HOUR),
  },
  {
    id: 'this_weekend',
    label: 'This weekend',
    // The coming Saturday morning. Null across the weekend itself — Saturday
    // and Sunday ARE the weekend, so "this weekend" has nothing left to point
    // at, and pointing at the NEXT one would be a different promise than the
    // label makes.
    //
    // Sunday needs its own arm: getDay() is 0 there, so `6 - getDay()` yields
    // 6 and would silently offer next Saturday.
    resolve: (now) => {
      const SATURDAY = 6
      const SUNDAY = 0
      const day = now.getDay()
      if (day === SATURDAY || day === SUNDAY) return null
      return atHour(addDays(now, SATURDAY - day), MORNING_HOUR)
    },
  },
  {
    id: 'next_week',
    label: 'Next week',
    resolve: (now) => atHour(addDays(now, daysUntilNextMonday(now)), MORNING_HOUR),
  },
  {
    id: 'next_month',
    label: 'Next month',
    // The 1st of next month. Built from year/month arithmetic rather than
    // "+30 days" so it lands on a real month boundary, and via month+1 with
    // day=1, which Date normalizes across a year end (December → January).
    resolve: (now) => new Date(now.getFullYear(), now.getMonth() + 1, 1, MORNING_HOUR, 0, 0, 0),
  },
]

/** One preset with its resolved instant — only those currently offerable. */
export interface ResolvedPreset {
  id: string
  label: string
  at: Date
}

/**
 * The presets offerable at `now`: those that resolve to a future instant
 * within the API's 90-day horizon.
 *
 * `now` is a parameter, not read from the clock, so the menu's contents are
 * testable at a fixed instant and every preset in one render resolves against
 * the same moment.
 */
export function offerablePresets(now: Date): ResolvedPreset[] {
  const horizon = new Date(now.getFullYear(), now.getMonth(), now.getDate() + SNOOZE_MAX_DAYS)
  return SNOOZE_PRESETS.flatMap((preset) => {
    const at = preset.resolve(now)
    if (!at || at <= now || at > horizon) return []
    return [{ id: preset.id, label: preset.label, at }]
  })
}

/**
 * A preset's instant as the `datetime-local` input's value — the format that
 * control requires (`YYYY-MM-DDTHH:mm`, local time, no zone). Hand-built
 * rather than sliced off `toISOString()`, which would convert to UTC and show
 * the wrong wall-clock time to anyone not on it.
 */
export function toDateTimeLocalValue(at: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}`
}

/** Validation result for a custom (typed) snooze moment. */
export type CustomSnoozeResult = { ok: true; at: Date } | { ok: false; reason: string }

/**
 * Validates a `datetime-local` value against the same two bounds the API
 * enforces, so the operator sees the problem inline instead of as a 422.
 * Client-side validation is a courtesy, never the enforcement — the server
 * checks independently.
 */
export function parseCustomSnooze(value: string, now: Date): CustomSnoozeResult {
  if (!value) return { ok: false, reason: 'Pick a date and time.' }
  const at = new Date(value)
  if (Number.isNaN(at.getTime())) return { ok: false, reason: "That date and time couldn't be read." }
  if (at <= now) return { ok: false, reason: 'Pick a moment in the future.' }
  const horizon = new Date(now.getFullYear(), now.getMonth(), now.getDate() + SNOOZE_MAX_DAYS)
  if (at > horizon) return { ok: false, reason: `Snoozing is limited to ${SNOOZE_MAX_DAYS} days ahead.` }
  return { ok: true, at }
}

/**
 * How a snoozed thread's return time reads in the UI: a weekday and time
 * inside the next week ("Mon 09:00"), a date beyond that ("12 Oct").
 * Intl handles the locale; the branch is about which fields are worth showing.
 */
export function formatSnoozeUntil(at: Date, now: Date): string {
  const withinAWeek = at.getTime() - now.getTime() < 7 * 24 * 60 * 60 * 1000
  return withinAWeek
    ? at.toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' })
    : at.toLocaleDateString(undefined, { day: 'numeric', month: 'short' })
}
