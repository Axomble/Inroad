// Geometry for the calendar-style send-window editor: minute intervals per
// weekday, snapping, and the drag arithmetic. Component-free so every rule is
// unit-tested directly — the same split schedule-draft.ts and schedule-time.ts
// already use.
import type { SendWindowDay } from './api'
import { MINUTES_PER_DAY } from './schedule-time'

/** Everything snaps to half hours. Finer than an hour grid, coarse enough to hit. */
export const SNAP_MINUTES = 30

/**
 * The shortest window a drag can create. A 30-minute flick is almost always a
 * mis-click rather than an intended half-hour window, so a too-short draw is
 * grown to an hour on release instead of being discarded — losing the gesture
 * entirely is more annoying than adjusting it.
 */
export const MIN_DRAW_MINUTES = 60

/** One sending window. `end` is EXCLUSIVE and may be 1440 (end of day). */
export interface Block {
  start: number
  end: number
}

/**
 * The editor's week, in DISPLAY order — index 0 is Monday.
 *
 * The API is Sunday-first (`weekday` 0 = Sunday, matching Go's `time.Weekday`),
 * but a working week reads Mon–Sun. The conversion lives in this module rather
 * than the component so exactly one place knows about the offset.
 */
export type BlockWeek = Block[][]

export function emptyWeek(): BlockWeek {
  return [[], [], [], [], [], [], []]
}

/** Display index (Mon=0) → API weekday (Sun=0). */
export function displayToApiWeekday(display: number): number {
  return (display + 1) % 7
}

/** API weekday (Sun=0) → display index (Mon=0). */
export function apiToDisplayWeekday(weekday: number): number {
  return (weekday + 6) % 7
}

export function clampMinute(n: number, lo = 0, hi = MINUTES_PER_DAY): number {
  return Math.max(lo, Math.min(hi, n))
}

/** Rounds to the nearest half hour. */
export function snapMinute(n: number): number {
  return Math.round(n / SNAP_MINUTES) * SNAP_MINUTES
}

/**
 * Sorts a day's windows and merges any that OVERLAP.
 *
 * Deliberately merges on `<` rather than `<=`: two windows that merely touch
 * (09:00–12:00 and 12:00–17:00) are left alone. The API's ranges are half-open
 * and the database's exclusion constraint agrees, so touching windows are
 * legal and distinct — an operator who split a day around lunch and then closed
 * the gap probably still wants two rows. Only a genuine overlap has to collapse,
 * because the constraint would reject it.
 */
export function mergeBlocks(blocks: Block[]): Block[] {
  const sorted = blocks.filter((b) => b.end > b.start).sort((a, b) => a.start - b.start)
  const out: Block[] = []
  for (const block of sorted) {
    const last = out[out.length - 1]
    if (last && block.start < last.end) {
      last.end = Math.max(last.end, block.end)
    } else {
      out.push({ ...block })
    }
  }
  return out
}

/** API days → the editor's Monday-first week. */
export function toBlockWeek(days: SendWindowDay[] | undefined): BlockWeek {
  const week = emptyWeek()
  for (const day of days ?? []) {
    const column = week[apiToDisplayWeekday(day.weekday)]
    if (!column) continue // an out-of-range weekday is ignored, not a crash
    for (const interval of day.intervals) {
      column.push({ start: interval.start_minute, end: interval.end_minute })
    }
  }
  return week.map(mergeBlocks)
}

/**
 * The editor's week → the API's days.
 *
 * A closed day is OMITTED rather than sent with an empty array: the API treats
 * an absent weekday as closed, so an empty array would be a second way of
 * saying the same thing. Days come out in API weekday order.
 */
export function fromBlockWeek(week: BlockWeek): SendWindowDay[] {
  const days: SendWindowDay[] = []
  for (const [display, blocks] of week.entries()) {
    const merged = mergeBlocks(blocks)
    if (merged.length === 0) continue
    days.push({
      weekday: displayToApiWeekday(display),
      intervals: merged.map((b) => ({ start_minute: b.start, end_minute: b.end })),
    })
  }
  return days.sort((a, b) => a.weekday - b.weekday)
}

/** Total open minutes across the week — for the summary line. */
export function openMinutes(week: BlockWeek): number {
  return week.reduce((total, day) => total + day.reduce((sum, b) => sum + (b.end - b.start), 0), 0)
}

export function hasAnyBlock(week: BlockWeek): boolean {
  return week.some((day) => day.length > 0)
}

/** A default 9–5 window, for the "add" affordance that needs no drag. */
export function defaultBlock(): Block {
  return { start: 9 * 60, end: 17 * 60 }
}

/**
 * A block being drawn between a fixed anchor and the pointer's current minute.
 * Always at least SNAP_MINUTES tall so a block exists to render mid-gesture.
 */
export function drawBlock(anchorMinute: number, currentMinute: number): Block {
  const snapped = snapMinute(currentMinute)
  const start = clampMinute(Math.min(anchorMinute, snapped), 0, MINUTES_PER_DAY - SNAP_MINUTES)
  const end = clampMinute(Math.max(anchorMinute, snapped), start + SNAP_MINUTES, MINUTES_PER_DAY)
  return { start, end }
}

/** Grows a too-short drawn block to MIN_DRAW_MINUTES, keeping it inside the day. */
export function growToMinimum(block: Block): Block {
  if (block.end - block.start >= MIN_DRAW_MINUTES) return block
  const end = Math.min(block.start + MIN_DRAW_MINUTES, MINUTES_PER_DAY)
  const start = Math.max(0, end - MIN_DRAW_MINUTES)
  return { start, end }
}

/** Slides a block by `deltaMinutes`, preserving its length and staying in-day. */
export function moveBlock(block: Block, deltaMinutes: number): Block {
  const length = block.end - block.start
  const start = clampMinute(snapMinute(block.start + deltaMinutes), 0, MINUTES_PER_DAY - length)
  return { start, end: start + length }
}

/** Drags the top edge, never past its own bottom. */
export function resizeStart(block: Block, deltaMinutes: number): Block {
  return { start: clampMinute(snapMinute(block.start + deltaMinutes), 0, block.end - SNAP_MINUTES), end: block.end }
}

/** Drags the bottom edge, never above its own top. */
export function resizeEnd(block: Block, deltaMinutes: number): Block {
  return {
    start: block.start,
    end: clampMinute(snapMinute(block.end + deltaMinutes), block.start + SNAP_MINUTES, MINUTES_PER_DAY),
  }
}

/** Copies one day's windows onto every other day — the day header's action. */
export function copyDayToAll(week: BlockWeek, fromDisplay: number): BlockWeek {
  const source = week[fromDisplay] ?? []
  // Each day gets its OWN block objects, not shared references: dragging one
  // day's window afterwards must not silently move the same window on the other
  // six. Written as an explicit construction rather than a spread so the copy is
  // obviously deep by one level, which is all Block needs.
  return week.map(() => source.map((b) => ({ start: b.start, end: b.end })))
}

/**
 * A window's label, e.g. "9am – 5pm" or "9:30am – midnight".
 *
 * 1440 renders as "midnight", not "12pm": an end of 1440 is the end of the day,
 * and a naive `hour < 12` test would show a fully-open day as "12am – 12pm",
 * reading as though sending stopped at noon.
 */
export function formatMinute(minute: number): string {
  if (minute >= MINUTES_PER_DAY) return 'midnight'
  const hour = Math.floor(minute / 60)
  const min = minute % 60
  const suffix = hour < 12 ? 'am' : 'pm'
  const hour12 = hour % 12 === 0 ? 12 : hour % 12
  return min === 0 ? `${hour12}${suffix}` : `${hour12}:${String(min).padStart(2, '0')}${suffix}`
}

export function formatBlock(block: Block): string {
  return `${formatMinute(block.start)} – ${formatMinute(block.end)}`
}

/** Weekday labels in DISPLAY order (Monday first). */
export const DISPLAY_WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const

/**
 * Full weekday names in DISPLAY order, for accessible labels. The short forms
 * above are for the visible column headers, where space is tight; a screen
 * reader should hear "Monday", not "Mon".
 */
export const DISPLAY_WEEKDAYS_LONG = [
  'Monday',
  'Tuesday',
  'Wednesday',
  'Thursday',
  'Friday',
  'Saturday',
  'Sunday',
] as const

/** Hour ticks on the time axis, including the closing midnight. */
export const AXIS_HOURS = [0, 3, 6, 9, 12, 15, 18, 21, 24] as const
