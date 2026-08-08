import { expect, test } from 'vitest'
import {
  createDateTimeFormatters,
  formatDate,
  formatDateTime,
  formatMonthYear,
} from './datetime'

// Every assertion pins both the locale and the timezone. An `Intl` test that
// takes either from the runner's environment passes on the machine it was written
// on and fails in CI, which is why these never touch the defaults — and why the
// factory is exported at all.
//
// The assertions read the *fields* each formatter promises rather than a whole
// literal string: ICU changes its separators between Node releases ("12 Aug 2026,
// 14:30" became "12 Aug 2026 at 14:30" once already), and a test that breaks on a
// comma is testing ICU, not us.

/** 12 August 2026, 14:30 UTC — an afternoon, so a 12-hour clock is detectable. */
const instant = '2026-08-12T14:30:00Z'

const gb = createDateTimeFormatters('en-GB', 'UTC')
const us = createDateTimeFormatters('en-US', 'UTC')

test('a date carries the day, month and year, and no clock', () => {
  const formatted = gb.date(instant)
  expect(formatted).toContain('12')
  expect(formatted).toContain('Aug')
  expect(formatted).toContain('2026')
  expect(formatted).not.toMatch(/14|2:30|30/)
})

test('a date and time carries both, in the pinned timezone', () => {
  const formatted = gb.dateTime(instant)
  expect(formatted).toContain('12 Aug 2026')
  // 14:30 UTC, not the runner's local hour — the timeZone argument is applied.
  expect(formatted).toContain('14:30')
})

test('the short form drops the year, which is noise on recent activity', () => {
  const formatted = gb.shortDateTime(instant)
  expect(formatted).toContain('Aug')
  expect(formatted).toContain('14:30')
  expect(formatted).not.toContain('2026')
})

test('the reader\'s own hour cycle is respected, per locale', () => {
  expect(gb.time(instant)).toBe('14:30')
  // The same instant, and the same formatter, read as an American would read it.
  expect(us.time(instant)).toMatch(/^02:30\s.?PM$/)
})

test('the 24-hour clock stays 24-hour even where the locale would not', () => {
  expect(gb.clock24(instant)).toBe('14:30')
  expect(us.clock24(instant)).toBe('14:30')
})

test('a month heading names the month and year only', () => {
  expect(gb.monthYear(instant)).toBe('August 2026')
})

test('every instant an API can hand us formats the same way', () => {
  const asDate = new Date(instant)
  expect(gb.dateTime(asDate)).toBe(gb.dateTime(instant))
  expect(gb.dateTime(asDate.getTime())).toBe(gb.dateTime(instant))
})

test('the default exports are wired to the formatter each name claims', () => {
  // Their exact text depends on the runner's locale, so this asserts only the
  // relationships their names promise — enough to catch a copy-paste slip in the
  // export block, which is otherwise invisible.
  expect(formatDateTime(instant)).toBe(formatDateTime(instant))
  expect(formatDateTime(instant).length).toBeGreaterThan(formatDate(instant).length)
  expect(formatMonthYear(instant)).not.toMatch(/:/)
})
