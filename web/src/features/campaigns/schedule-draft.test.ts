import { describe, expect, it } from 'vitest'
import { dailyLimitFromDraft, dailyLimitToDraft, fromDraft, newInterval, toDraft } from './schedule-draft'
import { minutesToTime, timeToMinutes } from './schedule-time'

describe('timeToMinutes', () => {
  it('parses valid times', () => {
    expect(timeToMinutes('00:00')).toBe(0)
    expect(timeToMinutes('09:30')).toBe(570)
    expect(timeToMinutes('23:59')).toBe(1439)
    expect(timeToMinutes(' 09:00 ')).toBe(540)
  })

  // A half-typed value must not be coerced — that would silently save a window
  // starting at midnight.
  it.each(['', '9', '9:', ':30', '24:00', '09:60', '-1:00', 'abc', '09:5'])('rejects %j', (value) => {
    expect(timeToMinutes(value)).toBeNull()
  })
})

describe('minutesToTime', () => {
  it('formats with zero padding', () => {
    expect(minutesToTime(0)).toBe('00:00')
    expect(minutesToTime(570)).toBe('09:30')
  })

  // 1440 is the API's exclusive-midnight maximum; no time input accepts "24:00".
  it('clamps exclusive midnight to the last representable minute', () => {
    expect(minutesToTime(1440)).toBe('23:59')
  })

  it('clamps negatives to midnight', () => {
    expect(minutesToTime(-30)).toBe('00:00')
  })
})

describe('toDraft', () => {
  it('places intervals in their weekday slot', () => {
    const week = toDraft([
      { weekday: 1, intervals: [{ start_minute: 540, end_minute: 720 }] },
      { weekday: 5, intervals: [{ start_minute: 600, end_minute: 900 }] },
    ])
    expect(week[1]).toHaveLength(1)
    expect(week[1]?.[0]).toMatchObject({ start: '09:00', end: '12:00' })
    expect(week[5]?.[0]).toMatchObject({ start: '10:00', end: '15:00' })
    expect(week[0]).toHaveLength(0)
  })

  it('handles a missing days list', () => {
    expect(toDraft(undefined)).toHaveLength(7)
  })

  // A weekday outside 0..6 is malformed data, not a reason to crash the editor.
  it('ignores an out-of-range weekday', () => {
    const week = toDraft([{ weekday: 9, intervals: [{ start_minute: 540, end_minute: 720 }] }])
    expect(week.flat()).toHaveLength(0)
  })

  it('gives every row a distinct key', () => {
    const week = toDraft([
      {
        weekday: 2,
        intervals: [
          { start_minute: 540, end_minute: 720 },
          { start_minute: 780, end_minute: 1020 },
        ],
      },
    ])
    const ids = week[2]?.map((iv) => iv.id) ?? []
    expect(new Set(ids).size).toBe(2)
  })
})

describe('fromDraft', () => {
  const empty = (): ReturnType<typeof toDraft> => [[], [], [], [], [], [], []]

  it('converts a valid draft to per-day payload groups', () => {
    const week = empty()
    week[1] = [newInterval('09:00', '12:00'), newInterval('13:00', '17:00')]
    week[3] = [newInterval('10:00', '11:30')]

    const result = fromDraft(week)
    expect('days' in result).toBe(true)
    if (!('days' in result)) return
    expect(result.days).toEqual([
      {
        weekday: 1,
        intervals: [
          { start_minute: 540, end_minute: 720 },
          { start_minute: 780, end_minute: 1020 },
        ],
      },
      { weekday: 3, intervals: [{ start_minute: 600, end_minute: 690 }] },
    ])
  })

  it('sorts each day so the payload is ordered regardless of entry order', () => {
    const week = empty()
    week[2] = [newInterval('14:00', '16:00'), newInterval('09:00', '10:00')]

    const result = fromDraft(week)
    if (!('days' in result)) throw new Error(result.problem)
    expect(result.days[0]?.intervals.map((iv) => iv.start_minute)).toEqual([540, 840])
  })

  it('rejects a week with nothing open', () => {
    const result = fromDraft(empty())
    expect(result).toEqual({ problem: expect.stringContaining('at least one sending window') })
  })

  it('rejects an incomplete time', () => {
    const week = empty()
    week[1] = [newInterval('09:00', '')]
    expect(fromDraft(week)).toEqual({ problem: expect.stringContaining('incomplete') })
  })

  it('rejects an end at or before the start', () => {
    const week = empty()
    week[4] = [newInterval('17:00', '09:00')]
    expect(fromDraft(week)).toEqual({ problem: expect.stringContaining('Thu') })

    const equal = empty()
    equal[4] = [newInterval('09:00', '09:00')]
    expect(fromDraft(equal)).toEqual({ problem: expect.stringContaining('after the start') })
  })

  it('rejects overlapping windows on the same day', () => {
    const week = empty()
    week[1] = [newInterval('09:00', '12:00'), newInterval('11:00', '15:00')]
    expect(fromDraft(week)).toEqual({ problem: expect.stringContaining('overlapping') })
  })

  // Half-open [start, end) means back-to-back windows touch but don't overlap.
  it('accepts adjacent windows', () => {
    const week = empty()
    week[1] = [newInterval('09:00', '12:00'), newInterval('12:00', '17:00')]
    expect('days' in fromDraft(week)).toBe(true)
  })

  it('names the offending day in the problem', () => {
    const week = empty()
    week[6] = [newInterval('10:00', '09:00')]
    expect(fromDraft(week)).toEqual({ problem: expect.stringContaining('Sat') })
  })
})

describe('dailyLimitToDraft', () => {
  it('shows a set limit as its digits', () => {
    expect(dailyLimitToDraft(250)).toBe('250')
    // Not filtered out as falsy: a 0 the server somehow returned must be visible
    // and rejected, not silently displayed as "no limit".
    expect(dailyLimitToDraft(0)).toBe('0')
  })

  it('shows no limit as an empty field', () => {
    expect(dailyLimitToDraft(null)).toBe('')
    expect(dailyLimitToDraft(undefined)).toBe('')
  })
})

describe('dailyLimitFromDraft', () => {
  it('reads an empty field as no campaign limit', () => {
    expect(dailyLimitFromDraft('')).toEqual({ dailyLimit: null })
    expect(dailyLimitFromDraft('   ')).toEqual({ dailyLimit: null })
  })

  it('reads digits as the limit', () => {
    expect(dailyLimitFromDraft('100')).toEqual({ dailyLimit: 100 })
    expect(dailyLimitFromDraft(' 1 ')).toEqual({ dailyLimit: 1 })
  })

  // The API's own minimum is 1; refusing here gives the specific reason instead
  // of a 422 the operator has to interpret.
  it.each(['0', '-5', '2.5', 'lots', '1e3', '1 000'])('refuses %j', (raw) => {
    expect(dailyLimitFromDraft(raw)).toEqual({ problem: expect.stringContaining('1 or more') })
  })
})
