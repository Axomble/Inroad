import { describe, expect, test } from 'vitest'
import {
  secondsUntil,
  showsCountdown,
  sendTimingLabel,
  COUNTDOWN_CEILING_SECONDS,
  PENDING_STATUS_LABELS,
} from '../undo-countdown'

const NOW = new Date(2026, 7, 26, 12, 0, 0)

/** An ISO instant `seconds` from NOW. */
function at(seconds: number): string {
  return new Date(NOW.getTime() + seconds * 1000).toISOString()
}

describe('secondsUntil', () => {
  test('counts whole seconds remaining', () => {
    expect(secondsUntil(at(30), NOW)).toBe(30)
  })

  test('rounds a partial second UP, so a countdown never shows 0 while time remains', () => {
    expect(secondsUntil(at(0.4), NOW)).toBe(1)
  })

  test('floors at zero rather than going negative', () => {
    expect(secondsUntil(at(-60), NOW)).toBe(0)
  })

  test('an unparseable instant reads as zero rather than NaN', () => {
    expect(secondsUntil('not-a-date', NOW)).toBe(0)
  })
})

describe('showsCountdown', () => {
  test('shows inside the window', () => {
    expect(showsCountdown(at(10), NOW)).toBe(true)
    expect(showsCountdown(at(COUNTDOWN_CEILING_SECONDS), NOW)).toBe(true)
  })

  // A far-future schedule would show a meaningless four-digit number, and a
  // ticking interval that runs for days.
  test('does not show beyond the ceiling', () => {
    expect(showsCountdown(at(COUNTDOWN_CEILING_SECONDS + 1), NOW)).toBe(false)
    expect(showsCountdown(at(86_400), NOW)).toBe(false)
  })

  test('does not show once due — there is nothing left to count', () => {
    expect(showsCountdown(at(0), NOW)).toBe(false)
    expect(showsCountdown(at(-5), NOW)).toBe(false)
  })
})

describe('sendTimingLabel', () => {
  test('counts down inside the window', () => {
    expect(sendTimingLabel(at(8), NOW)).toBe('Sending in 8s')
  })

  test('says it is going out once due', () => {
    expect(sendTimingLabel(at(-1), NOW)).toBe('Sending now')
  })

  test('names the moment for a real schedule rather than a huge countdown', () => {
    const label = sendTimingLabel(at(3 * 86_400), NOW)
    expect(label).toContain('Scheduled for')
    expect(label).not.toContain('Sending in')
  })

  test('degrades rather than throwing on an unparseable instant', () => {
    expect(sendTimingLabel('nonsense', NOW)).toBe('Sending soon')
  })
})

describe('PENDING_STATUS_LABELS', () => {
  // A Record over the status union: a status the API adds fails tsc here until
  // it is labelled, rather than rendering as a raw enum value.
  test('every status has distinct human copy', () => {
    const labels = Object.values(PENDING_STATUS_LABELS)
    expect(labels).toHaveLength(5)
    expect(new Set(labels).size).toBe(5)
    expect(labels.every(Boolean)).toBe(true)
  })

  // "Queued" not "Scheduled": the row is waiting out an undo window, and
  // "scheduled" reads as a deliberate future send, which it usually is not.
  test('a waiting reply reads as queued', () => {
    expect(PENDING_STATUS_LABELS.scheduled).toBe('Queued')
  })
})
