// Conversion between the API's minute intervals and the schedule board's
// hour cells. Component-free so the run-length encoding and the lossiness
// detection are unit-tested directly — the same split schedule-draft.ts and
// schedule-time.ts already use.
import type { SendWindowDay } from './api'
import { MINUTES_PER_DAY } from './schedule-time'

/** Hours in a day — the board's column count. */
export const HOURS_PER_DAY = 24

/** Days in a week — the board's row count. Indexed 0 = Sunday, as the API is. */
export const DAYS_PER_WEEK = 7

/**
 * The board's selection: `cells[weekday][hour]` is true when that hour is open.
 *
 * A dense boolean grid rather than a Set of "d:h" keys, because every consumer
 * wants random access by coordinate (rendering a cell, drag-painting a range)
 * and the grid is only 168 entries.
 */
export type CellGrid = boolean[][]

export function emptyGrid(): CellGrid {
  return Array.from({ length: DAYS_PER_WEEK }, () => Array.from({ length: HOURS_PER_DAY }, () => false))
}

/** A copy, so callers can mutate a draft without touching the previous state. */
export function cloneGrid(grid: CellGrid): CellGrid {
  return grid.map((row) => [...row])
}

/**
 * Whether a saved schedule can be represented on an hour grid at all.
 *
 * This is the question the board must ask BEFORE showing itself. An hour cell
 * cannot express 09:30–17:15, so rendering such a schedule as cells and saving
 * it back would silently round away minute precision the operator chose
 * deliberately — destroying their configuration to satisfy the UI. When this
 * returns false the board hands the campaign to the time-input editor instead.
 *
 * A 1440 end (exclusive midnight) IS hour-aligned: it is the boundary of the
 * 23:00 cell.
 */
export function isHourAligned(days: SendWindowDay[] | undefined): boolean {
  for (const day of days ?? []) {
    for (const interval of day.intervals) {
      if (interval.start_minute % 60 !== 0) return false
      if (interval.end_minute % 60 !== 0) return false
    }
  }
  return true
}

/**
 * Projects saved intervals onto the grid.
 *
 * Only meaningful when `isHourAligned` holds; a non-aligned interval is
 * floor/ceil'd here so the grid still renders something sane if a caller
 * ignores that check, but the caller is expected not to.
 */
export function intervalsToCells(days: SendWindowDay[] | undefined): CellGrid {
  const grid = emptyGrid()
  for (const day of days ?? []) {
    const row = grid[day.weekday]
    if (!row) continue // an out-of-range weekday is ignored, not a crash
    for (const interval of day.intervals) {
      const firstHour = Math.floor(interval.start_minute / 60)
      // The end is EXCLUSIVE, so an interval ending at 17:00 covers up to the
      // 16:00 cell. Ceil rather than floor for a non-aligned end, so 17:15
      // still shows the 17:00 cell as open rather than hiding open time.
      const lastHour = Math.ceil(interval.end_minute / 60) - 1
      for (let hour = Math.max(0, firstHour); hour <= Math.min(HOURS_PER_DAY - 1, lastHour); hour += 1) {
        row[hour] = true
      }
    }
  }
  return grid
}

/**
 * Collapses the grid back into the API's per-day intervals, run-length encoding
 * consecutive open hours into ONE interval each.
 *
 * The merge is not cosmetic. Emitting an interval per cell would write eight
 * rows for a 9–5 day instead of one, and — because the database's exclusion
 * constraint uses `int4range(start, end) WITH &&` on half-open ranges — eight
 * adjacent ranges are fine but pointless, while the merged form is what every
 * other writer produces and what the preview reads.
 *
 * A day's last cell ends at 1440, exclusive midnight. That value is deliberately
 * emitted directly rather than routed through `minutesToTime`, which clamps 1440
 * to "23:59", or `timeToMinutes`, which rejects hour 24 outright — going through
 * either would silently shorten the final hour by a minute.
 */
export function cellsToIntervals(grid: CellGrid): SendWindowDay[] {
  const days: SendWindowDay[] = []

  for (const [weekday, row] of grid.entries()) {
    const intervals: { start_minute: number; end_minute: number }[] = []
    let runStart: number | null = null

    for (let hour = 0; hour < HOURS_PER_DAY; hour += 1) {
      const open = row[hour] === true
      if (open && runStart === null) runStart = hour
      // Close the run on the first shut cell, or at the end of the day.
      if (!open && runStart !== null) {
        intervals.push({ start_minute: runStart * 60, end_minute: hour * 60 })
        runStart = null
      }
    }
    if (runStart !== null) {
      intervals.push({ start_minute: runStart * 60, end_minute: MINUTES_PER_DAY })
    }

    // A closed day is OMITTED rather than sent with an empty array: the API
    // treats an absent weekday as closed, and sending `intervals: []` would be
    // a second way of saying the same thing.
    if (intervals.length > 0) days.push({ weekday, intervals })
  }

  return days
}

/** Total open hours on the board — for the "N hours a week" summary. */
export function openHourCount(grid: CellGrid): number {
  return grid.reduce((total, row) => total + row.filter(Boolean).length, 0)
}

/** Whether anything at all is open. Nothing open is a 422 from the API. */
export function hasAnyOpenHour(grid: CellGrid): boolean {
  return grid.some((row) => row.some(Boolean))
}

/**
 * A rectangular paint from one cell to another, inclusive of both.
 *
 * Rectangular rather than reading-order: dragging from Mon 9am to Fri 5pm means
 * "those hours on those days", which is how a working week is actually
 * described. A reading-order sweep would also select Monday's evening and
 * Friday's morning, which nobody wants.
 */
export function paintRange(
  grid: CellGrid,
  from: { day: number; hour: number },
  to: { day: number; hour: number },
  open: boolean,
): CellGrid {
  const next = cloneGrid(grid)
  const [dayLo, dayHi] = from.day <= to.day ? [from.day, to.day] : [to.day, from.day]
  const [hourLo, hourHi] = from.hour <= to.hour ? [from.hour, to.hour] : [to.hour, from.hour]

  for (let day = dayLo; day <= dayHi; day += 1) {
    const row = next[day]
    if (!row) continue
    for (let hour = hourLo; hour <= hourHi; hour += 1) {
      row[hour] = open
    }
  }
  return next
}

/** Sets a whole weekday open or shut — the row header's action. */
export function setDay(grid: CellGrid, day: number, open: boolean): CellGrid {
  const next = cloneGrid(grid)
  const row = next[day]
  if (row) {
    for (let hour = 0; hour < HOURS_PER_DAY; hour += 1) row[hour] = open
  }
  return next
}

/** Sets one hour across every day — the column header's action. */
export function setHour(grid: CellGrid, hour: number, open: boolean): CellGrid {
  const next = cloneGrid(grid)
  for (const row of next) {
    if (hour >= 0 && hour < HOURS_PER_DAY) row[hour] = open
  }
  return next
}

/** A compact "9am"/"12pm"/"5pm" label for the hour axis. */
export function hourLabel(hour: number): string {
  if (hour === 0) return '12am'
  if (hour === 12) return '12pm'
  return hour < 12 ? `${hour}am` : `${hour - 12}pm`
}
