import { describe, expect, test } from 'vitest'
import {
  emptyGrid,
  intervalsToCells,
  cellsToIntervals,
  isHourAligned,
  paintRange,
  setDay,
  setHour,
  openHourCount,
  hasAnyOpenHour,
  hourLabel,
  HOURS_PER_DAY,
  DAYS_PER_WEEK,
} from '../schedule-cells'
import { MINUTES_PER_DAY } from '../schedule-time'
import type { SendWindowDay } from '../api'

/** Business hours on one weekday, the shape the migration backfills. */
function nineToFive(weekday: number): SendWindowDay {
  return { weekday, intervals: [{ start_minute: 540, end_minute: 1020 }] }
}

describe('isHourAligned', () => {
  // This is the gate that protects an operator's minute-precision schedule from
  // being silently rounded away by an hour grid.
  test('accepts hour boundaries', () => {
    expect(isHourAligned([nineToFive(1)])).toBe(true)
  })

  test('accepts an exclusive-midnight end', () => {
    expect(isHourAligned([{ weekday: 1, intervals: [{ start_minute: 1380, end_minute: 1440 }] }])).toBe(true)
  })

  test('rejects a minute-precision start or end', () => {
    expect(isHourAligned([{ weekday: 1, intervals: [{ start_minute: 570, end_minute: 1020 }] }])).toBe(false)
    expect(isHourAligned([{ weekday: 1, intervals: [{ start_minute: 540, end_minute: 1035 }] }])).toBe(false)
  })

  test('an empty or absent schedule is trivially alignable', () => {
    expect(isHourAligned([])).toBe(true)
    expect(isHourAligned(undefined)).toBe(true)
  })
})

describe('intervalsToCells', () => {
  test('opens exactly the covered hours, end exclusive', () => {
    const grid = intervalsToCells([nineToFive(1)])
    expect(grid[1]?.[8]).toBe(false) // 08:00 is before the window
    expect(grid[1]?.[9]).toBe(true)
    expect(grid[1]?.[16]).toBe(true) // 16:00–17:00 is the last open hour
    expect(grid[1]?.[17]).toBe(false) // 17:00 end is EXCLUSIVE
  })

  test('leaves other days shut', () => {
    const grid = intervalsToCells([nineToFive(1)])
    expect(grid[0]?.some(Boolean)).toBe(false)
    expect(grid[2]?.some(Boolean)).toBe(false)
  })

  test('handles several disjoint intervals on one day', () => {
    const grid = intervalsToCells([
      { weekday: 3, intervals: [{ start_minute: 540, end_minute: 720 }, { start_minute: 780, end_minute: 1020 }] },
    ])
    expect(grid[3]?.[9]).toBe(true)
    expect(grid[3]?.[11]).toBe(true)
    expect(grid[3]?.[12]).toBe(false) // the 12:00 lunch gap
    expect(grid[3]?.[13]).toBe(true)
  })

  test('an out-of-range weekday is ignored rather than crashing the board', () => {
    expect(() => intervalsToCells([{ weekday: 9, intervals: [{ start_minute: 0, end_minute: 60 }] }])).not.toThrow()
  })

  // Defensive: the board is not supposed to render a non-aligned schedule, but
  // if a caller skips isHourAligned it must not HIDE open time.
  test('a non-aligned interval is widened, never narrowed', () => {
    const grid = intervalsToCells([{ weekday: 1, intervals: [{ start_minute: 570, end_minute: 1035 }] }])
    expect(grid[1]?.[9]).toBe(true) // 09:30 start still shows the 9am cell
    expect(grid[1]?.[17]).toBe(true) // 17:15 end still shows the 5pm cell
  })
})

describe('cellsToIntervals', () => {
  test('run-length encodes consecutive hours into ONE interval', () => {
    const grid = emptyGrid()
    for (let hour = 9; hour < 17; hour += 1) grid[1]![hour] = true

    const days = cellsToIntervals(grid)
    expect(days).toHaveLength(1)
    // Eight cells become one interval, not eight.
    expect(days[0]).toEqual({ weekday: 1, intervals: [{ start_minute: 540, end_minute: 1020 }] })
  })

  test('a gap splits the day into separate intervals', () => {
    const grid = emptyGrid()
    for (const hour of [9, 10, 11, 13, 14]) grid[3]![hour] = true

    const days = cellsToIntervals(grid)
    expect(days[0]?.intervals).toEqual([
      { start_minute: 540, end_minute: 720 },
      { start_minute: 780, end_minute: 900 },
    ])
  })

  // The trap: minutesToTime clamps 1440 to "23:59" and timeToMinutes rejects
  // hour 24, so routing the last cell through either would silently shorten the
  // final hour by a minute.
  test('the last cell of the day ends at exclusive midnight, not 23:59', () => {
    const grid = emptyGrid()
    grid[6]![23] = true

    const days = cellsToIntervals(grid)
    expect(days[0]?.intervals).toEqual([{ start_minute: 1380, end_minute: MINUTES_PER_DAY }])
    expect(days[0]?.intervals[0]?.end_minute).toBe(1440)
  })

  test('a fully open day is one interval spanning midnight to midnight', () => {
    const grid = emptyGrid()
    for (let hour = 0; hour < HOURS_PER_DAY; hour += 1) grid[2]![hour] = true

    expect(cellsToIntervals(grid)[0]?.intervals).toEqual([{ start_minute: 0, end_minute: 1440 }])
  })

  // The API treats an absent weekday as closed, so sending `intervals: []`
  // would be a second way of saying the same thing.
  test('a closed day is omitted, not sent empty', () => {
    const grid = emptyGrid()
    grid[1]![9] = true

    const days = cellsToIntervals(grid)
    expect(days.map((d) => d.weekday)).toEqual([1])
  })

  test('an entirely closed week produces no days', () => {
    expect(cellsToIntervals(emptyGrid())).toEqual([])
  })
})

describe('round trip', () => {
  // The property that matters: an hour-aligned schedule must survive the board
  // unchanged, or opening the editor and saving without edits would rewrite it.
  test('an hour-aligned schedule survives cells and back unchanged', () => {
    const original: SendWindowDay[] = [
      nineToFive(1),
      nineToFive(2),
      { weekday: 6, intervals: [{ start_minute: 600, end_minute: 720 }] },
      { weekday: 0, intervals: [{ start_minute: 1380, end_minute: 1440 }] },
    ]

    const back = cellsToIntervals(intervalsToCells(original))
    // Sorted by weekday on the way out, so compare as sets of weekdays first.
    expect(back.map((d) => d.weekday)).toEqual([0, 1, 2, 6])
    expect(back.find((d) => d.weekday === 1)?.intervals).toEqual([{ start_minute: 540, end_minute: 1020 }])
    expect(back.find((d) => d.weekday === 6)?.intervals).toEqual([{ start_minute: 600, end_minute: 720 }])
    expect(back.find((d) => d.weekday === 0)?.intervals).toEqual([{ start_minute: 1380, end_minute: 1440 }])
  })

  test('adjacent intervals merge on the way back, which is the intended normalisation', () => {
    // Two touching intervals are indistinguishable from one once projected onto
    // cells — and the merged form is what every other writer produces.
    const touching: SendWindowDay[] = [
      { weekday: 1, intervals: [{ start_minute: 540, end_minute: 720 }, { start_minute: 720, end_minute: 1020 }] },
    ]
    expect(cellsToIntervals(intervalsToCells(touching))[0]?.intervals).toEqual([
      { start_minute: 540, end_minute: 1020 },
    ])
  })
})

describe('paintRange', () => {
  test('paints a rectangle inclusive of both corners', () => {
    const grid = paintRange(emptyGrid(), { day: 1, hour: 9 }, { day: 3, hour: 11 }, true)
    expect(grid[1]?.[9]).toBe(true)
    expect(grid[3]?.[11]).toBe(true)
    expect(grid[2]?.[10]).toBe(true)
    // Outside the rectangle stays shut — this is the whole point of a
    // rectangular paint over a reading-order sweep.
    expect(grid[1]?.[8]).toBe(false)
    expect(grid[4]?.[10]).toBe(false)
  })

  test('a backwards drag paints the same rectangle', () => {
    const forward = paintRange(emptyGrid(), { day: 1, hour: 9 }, { day: 3, hour: 11 }, true)
    const backward = paintRange(emptyGrid(), { day: 3, hour: 11 }, { day: 1, hour: 9 }, true)
    expect(backward).toEqual(forward)
  })

  test('painting shut clears only the rectangle', () => {
    let grid = setDay(emptyGrid(), 1, true)
    grid = paintRange(grid, { day: 1, hour: 9 }, { day: 1, hour: 11 }, false)
    expect(grid[1]?.[8]).toBe(true)
    expect(grid[1]?.[9]).toBe(false)
    expect(grid[1]?.[12]).toBe(true)
  })

  test('does not mutate its input', () => {
    const before = emptyGrid()
    paintRange(before, { day: 0, hour: 0 }, { day: 6, hour: 23 }, true)
    expect(hasAnyOpenHour(before)).toBe(false)
  })
})

describe('setDay and setHour', () => {
  test('setDay opens one weekday entirely', () => {
    const grid = setDay(emptyGrid(), 4, true)
    expect(grid[4]?.every(Boolean)).toBe(true)
    expect(grid[3]?.some(Boolean)).toBe(false)
  })

  test('setHour opens one hour across every day', () => {
    const grid = setHour(emptyGrid(), 9, true)
    for (let day = 0; day < DAYS_PER_WEEK; day += 1) {
      expect(grid[day]?.[9]).toBe(true)
      expect(grid[day]?.[10]).toBe(false)
    }
  })

  test('neither mutates its input', () => {
    const before = emptyGrid()
    setDay(before, 1, true)
    setHour(before, 1, true)
    expect(hasAnyOpenHour(before)).toBe(false)
  })
})

describe('counts', () => {
  test('openHourCount totals the board', () => {
    expect(openHourCount(emptyGrid())).toBe(0)
    expect(openHourCount(intervalsToCells([nineToFive(1), nineToFive(2)]))).toBe(16)
  })

  test('hasAnyOpenHour distinguishes a closed week', () => {
    expect(hasAnyOpenHour(emptyGrid())).toBe(false)
    expect(hasAnyOpenHour(intervalsToCells([nineToFive(1)]))).toBe(true)
  })
})

describe('hourLabel', () => {
  test('reads as a clock, not a 24-hour index', () => {
    expect(hourLabel(0)).toBe('12am')
    expect(hourLabel(9)).toBe('9am')
    expect(hourLabel(12)).toBe('12pm')
    expect(hourLabel(17)).toBe('5pm')
    expect(hourLabel(23)).toBe('11pm')
  })
})
