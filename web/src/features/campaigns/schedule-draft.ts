// Draft state and validation for the send-window editor. Component-free so the
// rules live in one place and are unit-tested directly, and so the panel file
// only exports components (fast refresh).
import { httpStatus } from '@/lib/rtk-error'
import type { SendWindowDay } from './api'
import { WEEKDAY_SHORT, minutesToTime, timeToMinutes } from './schedule-time'

/**
 * A draft interval holds raw strings, not minutes: a half-typed "9:" must stay
 * invalid rather than being coerced to a window starting at 09:00. `id` is
 * client-only and exists to key the rows — intervals have no server identity.
 */
export type DraftInterval = { id: string; start: string; end: string }
export type DraftWeek = DraftInterval[][]

export const EMPTY_WEEK: DraftWeek = [[], [], [], [], [], [], []]

let intervalSeq = 0

/** A new interval row, defaulting to business hours. */
export function newInterval(start = '09:00', end = '17:00'): DraftInterval {
  intervalSeq += 1
  return { id: `iv-${intervalSeq}`, start, end }
}

/** Turns the API's per-day groups into the flat 7-slot editor state. */
export function toDraft(days: SendWindowDay[] | undefined): DraftWeek {
  const week: DraftWeek = [[], [], [], [], [], [], []]
  for (const day of days ?? []) {
    const slot = week[day.weekday]
    if (!slot) continue // out-of-range weekday: ignore rather than break the editor
    for (const iv of day.intervals) {
      slot.push(newInterval(minutesToTime(iv.start_minute), minutesToTime(iv.end_minute)))
    }
  }
  return week
}

/**
 * Validates the draft and converts it to the request payload, returning the
 * problem instead when it can't be saved — the editor explains it inline rather
 * than bouncing the operator off the API's 422.
 *
 * Mirrors the server's rules deliberately: the API (and a database exclusion
 * constraint) remain the authority, this is just the fast, specific feedback.
 */
export function fromDraft(week: DraftWeek): { days: SendWindowDay[] } | { problem: string } {
  const days: SendWindowDay[] = []

  for (const [weekday, intervals] of week.entries()) {
    if (intervals.length === 0) continue
    const parsed: { start_minute: number; end_minute: number }[] = []
    const label = WEEKDAY_SHORT[weekday]

    for (const iv of intervals) {
      const start = timeToMinutes(iv.start)
      const end = timeToMinutes(iv.end)
      if (start === null || end === null) {
        return { problem: `${label} has an incomplete or invalid time.` }
      }
      if (start >= end) {
        return { problem: `${label}: the end time must be after the start time.` }
      }
      parsed.push({ start_minute: start, end_minute: end })
    }

    parsed.sort((a, b) => a.start_minute - b.start_minute)
    for (const [i, iv] of parsed.entries()) {
      const prev = parsed[i - 1]
      if (prev && iv.start_minute < prev.end_minute) {
        return { problem: `${label} has overlapping windows.` }
      }
    }
    days.push({ weekday, intervals: parsed })
  }

  if (days.length === 0) {
    return { problem: 'Add at least one sending window — otherwise the campaign never sends.' }
  }
  return { days }
}

/**
 * The daily limit as the editor holds it: a raw string, empty meaning "no
 * limit". Same reasoning as the interval bounds — a half-cleared field must stay
 * empty rather than being coerced to 0, which the API would reject.
 */
/** Matches the contract's `maximum` on `daily_limit`, which the API also enforces. */
export const MAX_DAILY_LIMIT = 1_000_000

export function dailyLimitToDraft(limit: number | null | undefined): string {
  return typeof limit === 'number' ? String(limit) : ''
}

/**
 * Parses the daily-limit field: empty ⇄ `null` (no campaign limit), otherwise a
 * whole number of 1 or more. Below 1 is refused here so the operator gets the
 * specific reason instead of the API's 422.
 */
export function dailyLimitFromDraft(raw: string): { dailyLimit: number | null } | { problem: string } {
  const trimmed = raw.trim()
  if (trimmed === '') return { dailyLimit: null }
  if (!/^\d+$/.test(trimmed) || Number(trimmed) < 1) {
    return {
      problem: 'Daily limit must be a whole number of 1 or more — leave it empty for no campaign limit.',
    }
  }
  // Upper bound, not cosmetic: the column is a 32-bit integer, so an unbounded value
  // reaches Postgres out of range and surfaces as a 500 rather than a validation
  // error. It also stays inside the range JSON numbers represent exactly.
  if (Number(trimmed) > MAX_DAILY_LIMIT) {
    return { problem: `Daily limit must be ${MAX_DAILY_LIMIT.toLocaleString()} or less.` }
  }
  return { dailyLimit: Number(trimmed) }
}

/** Maps a schedule-save failure to human copy, mirroring the API's 422 reasons. */
export function scheduleErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  if (status === 422) {
    return 'That schedule is invalid — check for overlapping or inverted times, leave at least one window open, and keep the daily limit at 1 or more.'
  }
  if (status === 404) return 'This campaign no longer exists.'
  return "Couldn't save the schedule. Please try again."
}
