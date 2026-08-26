import { describe, expect, test } from 'vitest'
import { bucketFor, groupByBucket, THREAD_BUCKETS } from '../thread-buckets'

// A fixed "now" so every boundary assertion is exact rather than relative to
// the clock the suite happens to run at: Wednesday 2026-08-26, 15:00 local.
const NOW = new Date(2026, 7, 26, 15, 0, 0)

/** Local-time helper — these buckets are claims about the viewer's own day. */
function at(year: number, month: number, day: number, hour = 12, minute = 0): Date {
  return new Date(year, month, day, hour, minute)
}

describe('bucketFor', () => {
  test('anything at or after local midnight today is "today"', () => {
    expect(bucketFor(at(2026, 7, 26, 0, 0), NOW)).toBe('today')
    expect(bucketFor(at(2026, 7, 26, 14, 59), NOW)).toBe('today')
  })

  test('one minute before local midnight is yesterday, not today', () => {
    expect(bucketFor(at(2026, 7, 25, 23, 59), NOW)).toBe('yesterday')
  })

  test('yesterday spans its own whole local day', () => {
    expect(bucketFor(at(2026, 7, 25, 0, 0), NOW)).toBe('yesterday')
    expect(bucketFor(at(2026, 7, 24, 23, 59), NOW)).toBe('this_week')
  })

  test('the week starts Monday, so Monday of this week is "this week" and the Sunday before is not', () => {
    // 2026-08-26 is a Wednesday; Monday is the 24th.
    expect(bucketFor(at(2026, 7, 24), NOW)).toBe('this_week')
    expect(bucketFor(at(2026, 7, 23), NOW)).toBe('this_month')
  })

  test('earlier in the same calendar month falls in "this month"', () => {
    expect(bucketFor(at(2026, 7, 1), NOW)).toBe('this_month')
  })

  test('the last day of the previous month is "earlier"', () => {
    expect(bucketFor(at(2026, 6, 31), NOW)).toBe('earlier')
  })

  // A Sunday "now" is the case a naive getDay()-based week start gets wrong:
  // Sunday is day 0, so an unadjusted implementation would treat it as the
  // start of the week and put the preceding Mon–Sat in an older bucket.
  test('on a Sunday, the week still starts on the preceding Monday', () => {
    const sunday = new Date(2026, 7, 30, 12, 0, 0)
    expect(bucketFor(at(2026, 7, 24), sunday)).toBe('this_week')
    expect(bucketFor(at(2026, 7, 23), sunday)).toBe('this_month')
  })

  test('a future timestamp buckets as today rather than falling through to "earlier"', () => {
    expect(bucketFor(at(2026, 7, 27), NOW)).toBe('today')
  })
})

describe('groupByBucket', () => {
  const item = (id: string, date: Date) => ({ id, at: date.toISOString() })

  test('omits empty buckets and keeps them in chronological order', () => {
    const groups = groupByBucket(
      [item('a', at(2026, 7, 26)), item('b', at(2026, 6, 1)), item('c', at(2026, 7, 25))],
      (i) => i.at,
      NOW,
    )
    expect(groups.map((g) => g.bucket)).toEqual(['today', 'yesterday', 'earlier'])
  })

  test('preserves input order within a bucket', () => {
    const groups = groupByBucket(
      [item('first', at(2026, 7, 26, 14)), item('second', at(2026, 7, 26, 9))],
      (i) => i.at,
      NOW,
    )
    expect(groups[0]?.items.map((i) => i.id)).toEqual(['first', 'second'])
  })

  test('an empty list yields no groups', () => {
    expect(groupByBucket([], (i: { at: string }) => i.at, NOW)).toEqual([])
  })

  test('an unparseable timestamp is bucketed as "earlier" rather than throwing', () => {
    const groups = groupByBucket([{ at: 'not-a-date' }], (i) => i.at, NOW)
    expect(groups.map((g) => g.bucket)).toEqual(['earlier'])
  })

  // One item per bucket, so every bucket is exercised and each must arrive
  // labelled — a missing entry in BUCKET_LABELS would surface as undefined.
  test('every bucket carries a distinct human label', () => {
    const oneEach = [
      at(2026, 7, 26), // today
      at(2026, 7, 25), // yesterday
      at(2026, 7, 24), // this week (Monday)
      at(2026, 7, 3), // this month
      at(2026, 5, 1), // earlier
    ].map((date) => ({ at: date.toISOString() }))

    const groups = groupByBucket(oneEach, (i) => i.at, NOW)

    expect(groups.map((g) => g.bucket)).toEqual([...THREAD_BUCKETS])
    const labels = groups.map((g) => g.label)
    expect(labels.every(Boolean)).toBe(true)
    expect(new Set(labels).size).toBe(THREAD_BUCKETS.length)
  })
})
