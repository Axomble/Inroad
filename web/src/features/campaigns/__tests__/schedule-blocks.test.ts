import { describe, expect, test } from 'vitest'
import {
  emptyWeek,
  toBlockWeek,
  fromBlockWeek,
  mergeBlocks,
  displayToApiWeekday,
  apiToDisplayWeekday,
  snapMinute,
  drawBlock,
  growToMinimum,
  moveBlock,
  resizeStart,
  resizeEnd,
  copyDayToAll,
  openMinutes,
  hasAnyBlock,
  formatMinute,
  formatBlock,
  SNAP_MINUTES,
  MIN_DRAW_MINUTES,
} from '../schedule-blocks'
import { MINUTES_PER_DAY } from '../schedule-time'
import type { SendWindowDay } from '../api'

describe('weekday mapping', () => {
  // The API is Sunday-first (Go's time.Weekday); the editor reads Mon-Sun.
  test('display Monday is API weekday 1', () => {
    expect(displayToApiWeekday(0)).toBe(1)
  })

  test('display Sunday is API weekday 0', () => {
    expect(displayToApiWeekday(6)).toBe(0)
  })

  test('the mapping round-trips for every day', () => {
    for (let display = 0; display < 7; display += 1) {
      expect(apiToDisplayWeekday(displayToApiWeekday(display))).toBe(display)
    }
    for (let weekday = 0; weekday < 7; weekday += 1) {
      expect(displayToApiWeekday(apiToDisplayWeekday(weekday))).toBe(weekday)
    }
  })
})

describe('mergeBlocks', () => {
  test('sorts by start', () => {
    expect(mergeBlocks([{ start: 600, end: 660 }, { start: 300, end: 360 }])).toEqual([
      { start: 300, end: 360 },
      { start: 600, end: 660 },
    ])
  })

  test('merges genuine overlaps, which the DB constraint would reject', () => {
    expect(mergeBlocks([{ start: 540, end: 720 }, { start: 600, end: 1020 }])).toEqual([
      { start: 540, end: 1020 },
    ])
  })

  // The deliberate departure from a naive merge: the API's ranges are half-open
  // and the exclusion constraint agrees, so touching windows are legal AND
  // distinct. An operator who split a day around lunch then closed the gap
  // probably still wants two rows.
  test('leaves merely TOUCHING windows alone', () => {
    const touching = [{ start: 540, end: 720 }, { start: 720, end: 1020 }]
    expect(mergeBlocks(touching)).toEqual(touching)
  })

  test('drops zero-length and inverted blocks', () => {
    expect(mergeBlocks([{ start: 600, end: 600 }, { start: 700, end: 650 }])).toEqual([])
  })

  test('collapses a chain of overlaps into one', () => {
    expect(
      mergeBlocks([
        { start: 0, end: 120 },
        { start: 60, end: 240 },
        { start: 200, end: 300 },
      ]),
    ).toEqual([{ start: 0, end: 300 }])
  })
})

describe('toBlockWeek / fromBlockWeek', () => {
  const saved: SendWindowDay[] = [
    { weekday: 1, intervals: [{ start_minute: 540, end_minute: 1020 }] }, // Monday
    { weekday: 0, intervals: [{ start_minute: 1380, end_minute: 1440 }] }, // Sunday
  ]

  test('places API weekdays in display order', () => {
    const week = toBlockWeek(saved)
    expect(week[0]).toEqual([{ start: 540, end: 1020 }]) // Monday first
    expect(week[6]).toEqual([{ start: 1380, end: 1440 }]) // Sunday last
  })

  // The property that matters: opening the editor and saving without edits must
  // not rewrite the schedule.
  test('round-trips a saved schedule unchanged', () => {
    expect(fromBlockWeek(toBlockWeek(saved))).toEqual([
      { weekday: 0, intervals: [{ start_minute: 1380, end_minute: 1440 }] },
      { weekday: 1, intervals: [{ start_minute: 540, end_minute: 1020 }] },
    ])
  })

  // Unlike an hour grid, this model has no lossy step — minute precision
  // survives, so the editor never has to be withheld.
  test('minute precision survives the round trip', () => {
    const precise: SendWindowDay[] = [
      { weekday: 3, intervals: [{ start_minute: 570, end_minute: 1035 }] },
    ]
    expect(fromBlockWeek(toBlockWeek(precise))).toEqual(precise)
  })

  test('a closed day is omitted, not sent empty', () => {
    const week = emptyWeek()
    week[0] = [{ start: 540, end: 1020 }]
    expect(fromBlockWeek(week).map((d) => d.weekday)).toEqual([1])
  })

  test('an entirely closed week produces no days', () => {
    expect(fromBlockWeek(emptyWeek())).toEqual([])
  })

  test('an out-of-range weekday is ignored rather than crashing', () => {
    expect(() => toBlockWeek([{ weekday: 9, intervals: [{ start_minute: 0, end_minute: 60 }] }])).not.toThrow()
  })
})

describe('snapMinute', () => {
  test('rounds to the nearest half hour', () => {
    expect(snapMinute(0)).toBe(0)
    expect(snapMinute(14)).toBe(0)
    expect(snapMinute(16)).toBe(30)
    expect(snapMinute(545)).toBe(540)
    // 555 is exactly half way (18.5 snaps), and Math.round breaks the tie
    // upward — so it lands on 570, not 540.
    expect(snapMinute(555)).toBe(570)
    expect(snapMinute(554)).toBe(540)
  })
})

describe('drawBlock', () => {
  test('draws downward from the anchor', () => {
    expect(drawBlock(540, 720)).toEqual({ start: 540, end: 720 })
  })

  test('draws upward from the anchor, keeping the anchor as the end', () => {
    expect(drawBlock(720, 540)).toEqual({ start: 540, end: 720 })
  })

  test('is never shorter than one snap, so a block exists mid-gesture', () => {
    const block = drawBlock(540, 540)
    expect(block.end - block.start).toBe(SNAP_MINUTES)
  })

  test('cannot escape the day', () => {
    expect(drawBlock(1440, 2000).end).toBeLessThanOrEqual(MINUTES_PER_DAY)
    expect(drawBlock(0, -500).start).toBeGreaterThanOrEqual(0)
  })
})

describe('growToMinimum', () => {
  // A 30-minute flick is nearly always a mis-click. Growing it beats discarding
  // the gesture, which is more annoying than adjusting the result.
  test('grows a too-short draw to the minimum', () => {
    expect(growToMinimum({ start: 540, end: 570 })).toEqual({ start: 540, end: 600 })
  })

  test('leaves a long-enough block alone', () => {
    const block = { start: 540, end: 1020 }
    expect(growToMinimum(block)).toEqual(block)
  })

  test('grows upward when there is no room below', () => {
    const grown = growToMinimum({ start: 1410, end: 1440 })
    expect(grown.end).toBe(MINUTES_PER_DAY)
    expect(grown.end - grown.start).toBe(MIN_DRAW_MINUTES)
  })
})

describe('moveBlock', () => {
  test('slides while preserving length', () => {
    expect(moveBlock({ start: 540, end: 1020 }, 60)).toEqual({ start: 600, end: 1080 })
  })

  test('stops at the end of the day rather than truncating', () => {
    const moved = moveBlock({ start: 1320, end: 1440 }, 600)
    expect(moved.end).toBe(MINUTES_PER_DAY)
    expect(moved.end - moved.start).toBe(120)
  })

  test('stops at midnight going up', () => {
    const moved = moveBlock({ start: 60, end: 180 }, -600)
    expect(moved.start).toBe(0)
    expect(moved.end - moved.start).toBe(120)
  })
})

describe('resize', () => {
  test('the top edge cannot pass the bottom', () => {
    expect(resizeStart({ start: 540, end: 600 }, 500).start).toBe(600 - SNAP_MINUTES)
  })

  test('the bottom edge cannot pass the top', () => {
    expect(resizeEnd({ start: 540, end: 600 }, -500).end).toBe(540 + SNAP_MINUTES)
  })

  test('resizing the top leaves the bottom fixed', () => {
    expect(resizeStart({ start: 540, end: 1020 }, 60)).toEqual({ start: 600, end: 1020 })
  })

  test('resizing the bottom leaves the top fixed', () => {
    expect(resizeEnd({ start: 540, end: 1020 }, 60)).toEqual({ start: 540, end: 1080 })
  })

  test('neither edge escapes the day', () => {
    expect(resizeStart({ start: 60, end: 1020 }, -500).start).toBe(0)
    expect(resizeEnd({ start: 540, end: 1380 }, 500).end).toBe(MINUTES_PER_DAY)
  })
})

describe('copyDayToAll', () => {
  test('replaces every day with the source day', () => {
    const week = emptyWeek()
    week[0] = [{ start: 540, end: 1020 }]

    const copied = copyDayToAll(week, 0)
    for (let day = 0; day < 7; day += 1) {
      expect(copied[day]).toEqual([{ start: 540, end: 1020 }])
    }
  })

  test('copies by value, so editing one day does not change the others', () => {
    const week = emptyWeek()
    week[0] = [{ start: 540, end: 1020 }]
    const copied = copyDayToAll(week, 0)

    copied[1]![0]!.start = 0
    expect(copied[2]?.[0]?.start).toBe(540)
  })

  test('copying an empty day closes the week', () => {
    const week = emptyWeek()
    week[0] = [{ start: 540, end: 1020 }]
    expect(hasAnyBlock(copyDayToAll(week, 3))).toBe(false)
  })
})

describe('summary helpers', () => {
  test('openMinutes totals the week', () => {
    const week = emptyWeek()
    week[0] = [{ start: 540, end: 1020 }] // 8h
    week[1] = [{ start: 540, end: 600 }] // 1h
    expect(openMinutes(week)).toBe(540)
  })

  test('hasAnyBlock distinguishes a closed week', () => {
    expect(hasAnyBlock(emptyWeek())).toBe(false)
  })
})

describe('formatMinute', () => {
  test('reads as a clock', () => {
    expect(formatMinute(0)).toBe('12am')
    expect(formatMinute(540)).toBe('9am')
    expect(formatMinute(720)).toBe('12pm')
    expect(formatMinute(1020)).toBe('5pm')
    expect(formatMinute(570)).toBe('9:30am')
  })

  // A naive hour<12 test renders 1440 as "12pm", so a fully-open day reads
  // "12am – 12pm" and looks like sending stopped at noon.
  test('the end of the day is midnight, not 12pm', () => {
    expect(formatMinute(MINUTES_PER_DAY)).toBe('midnight')
    expect(formatBlock({ start: 0, end: MINUTES_PER_DAY })).toBe('12am – midnight')
  })
})
