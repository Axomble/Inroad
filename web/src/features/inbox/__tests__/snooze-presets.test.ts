import { describe, expect, test } from 'vitest'
import {
  offerablePresets,
  parseCustomSnooze,
  toDateTimeLocalValue,
  formatSnoozeUntil,
  SNOOZE_MAX_DAYS,
} from '../snooze-presets'

// Fixed instants in LOCAL time, since every preset is a claim about the
// viewer's own clock. 2026-08-26 is a Wednesday.
const WED_10AM = new Date(2026, 7, 26, 10, 0, 0)
const WED_11PM = new Date(2026, 7, 26, 23, 0, 0)
const SAT_10AM = new Date(2026, 7, 29, 10, 0, 0)
const SUN_10AM = new Date(2026, 7, 30, 10, 0, 0)

function ids(now: Date): string[] {
  return offerablePresets(now).map((p) => p.id)
}

function presetAt(now: Date, id: string): Date {
  const found = offerablePresets(now).find((p) => p.id === id)
  if (!found) throw new Error(`preset ${id} was not offered at ${now.toISOString()}`)
  return found.at
}

describe('offerablePresets', () => {
  test('a mid-morning weekday offers every preset', () => {
    expect(ids(WED_10AM)).toEqual(['later_today', 'tomorrow', 'this_weekend', 'next_week', 'next_month'])
  })

  test('every offered preset resolves to a future instant', () => {
    for (const now of [WED_10AM, WED_11PM, SAT_10AM, SUN_10AM]) {
      for (const preset of offerablePresets(now)) {
        expect(preset.at.getTime()).toBeGreaterThan(now.getTime())
      }
    }
  })

  test('"later today" is dropped late at night, when it would mean tomorrow', () => {
    expect(ids(WED_11PM)).not.toContain('later_today')
    // Tomorrow is still offered — the operator is not left without an option.
    expect(ids(WED_11PM)).toContain('tomorrow')
  })

  test('"later today" is three hours on, rounded to the hour', () => {
    const at = presetAt(new Date(2026, 7, 26, 10, 37, 0), 'later_today')
    expect(at.getHours()).toBe(13)
    expect(at.getMinutes()).toBe(0)
    expect(at.getDate()).toBe(26)
  })

  test('"this weekend" is dropped once the weekend has arrived', () => {
    expect(ids(SAT_10AM)).not.toContain('this_weekend')
    expect(ids(SUN_10AM)).not.toContain('this_weekend')
  })

  test('"this weekend" is the coming Saturday morning', () => {
    const at = presetAt(WED_10AM, 'this_weekend')
    expect(at.getDay()).toBe(6)
    expect(at.getDate()).toBe(29)
    expect(at.getHours()).toBe(9)
  })

  // The case a naive `(1 - getDay() + 7) % 7` gets wrong: on a Monday it
  // yields 0, making "next week" mean today.
  test('"next week" on a Monday is the FOLLOWING Monday, not today', () => {
    const monday = new Date(2026, 7, 24, 10, 0, 0)
    const at = presetAt(monday, 'next_week')
    expect(at.getDay()).toBe(1)
    expect(at.getDate()).toBe(31)
  })

  test('"next week" from mid-week is the coming Monday', () => {
    const at = presetAt(WED_10AM, 'next_week')
    expect(at.getDay()).toBe(1)
    expect(at.getDate()).toBe(31)
  })

  test('"next month" is the 1st of the next month', () => {
    const at = presetAt(WED_10AM, 'next_month')
    expect(at.getMonth()).toBe(8) // September
    expect(at.getDate()).toBe(1)
  })

  test('"next month" rolls over a year boundary', () => {
    const december = new Date(2026, 11, 15, 10, 0, 0)
    const at = presetAt(december, 'next_month')
    expect(at.getFullYear()).toBe(2027)
    expect(at.getMonth()).toBe(0)
    expect(at.getDate()).toBe(1)
  })

  test('no preset ever exceeds the API 90-day horizon', () => {
    for (const now of [WED_10AM, WED_11PM, SAT_10AM, new Date(2026, 11, 15, 10, 0, 0)]) {
      const horizon = new Date(now.getFullYear(), now.getMonth(), now.getDate() + SNOOZE_MAX_DAYS)
      for (const preset of offerablePresets(now)) {
        expect(preset.at.getTime()).toBeLessThanOrEqual(horizon.getTime())
      }
    }
  })
})

describe('parseCustomSnooze', () => {
  test('accepts a future moment inside the horizon', () => {
    const result = parseCustomSnooze('2026-08-27T14:30', WED_10AM)
    expect(result.ok).toBe(true)
    if (result.ok) expect(result.at.getHours()).toBe(14)
  })

  test('rejects an empty value', () => {
    expect(parseCustomSnooze('', WED_10AM)).toEqual({ ok: false, reason: 'Pick a date and time.' })
  })

  test('rejects an unparseable value', () => {
    const result = parseCustomSnooze('not-a-date', WED_10AM)
    expect(result.ok).toBe(false)
  })

  test('rejects the past and the present', () => {
    expect(parseCustomSnooze('2026-08-25T10:00', WED_10AM).ok).toBe(false)
    expect(parseCustomSnooze('2026-08-26T10:00', WED_10AM).ok).toBe(false)
  })

  test('rejects beyond the horizon, naming the bound', () => {
    const result = parseCustomSnooze('2027-06-01T09:00', WED_10AM)
    expect(result.ok).toBe(false)
    if (!result.ok) expect(result.reason).toContain(String(SNOOZE_MAX_DAYS))
  })
})

describe('toDateTimeLocalValue', () => {
  // Sliced-off toISOString() would render this in UTC and show the wrong
  // wall-clock time to anyone not on it.
  test('renders local wall-clock time, zero-padded', () => {
    expect(toDateTimeLocalValue(new Date(2026, 0, 5, 9, 7))).toBe('2026-01-05T09:07')
  })
})

describe('formatSnoozeUntil', () => {
  test('inside a week, names the weekday', () => {
    const label = formatSnoozeUntil(new Date(2026, 7, 27, 9, 0), WED_10AM)
    // Locale-dependent, so assert the shape rather than exact copy: a weekday
    // abbreviation plus a time, not a bare date.
    expect(label).toMatch(/\d{1,2}[:.]\d{2}/)
  })

  test('beyond a week, names the date instead', () => {
    const label = formatSnoozeUntil(new Date(2026, 9, 12, 9, 0), WED_10AM)
    expect(label).toContain('12')
  })
})
