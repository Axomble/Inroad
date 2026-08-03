import { describe, expect, test } from 'vitest'
import type { DeliverabilityPoint } from '@/store/api'
import { hasPlottableHistory, panelValueLabel, seriesPanels } from './deliverability-series'

const NOW = Date.parse('2026-08-20T12:00:00Z')

function day(overrides: Partial<DeliverabilityPoint> & { date: string }): DeliverabilityPoint {
  return { delivered: 100, bounced: 2, complained: null, spam_placed: null, ...overrides }
}

const WEEK: DeliverabilityPoint[] = [
  day({ date: '2026-08-18', delivered: 200, bounced: 4, spam_placed: 6 }),
  day({ date: '2026-08-19', delivered: 250, bounced: 23, spam_placed: 5 }),
  day({ date: '2026-08-20', delivered: 180, bounced: 3, spam_placed: 0 }),
]

function panel(series: DeliverabilityPoint[], key: string) {
  const found = seriesPanels(series, NOW).find((p) => p.key === key)
  if (!found) throw new Error(`no panel ${key}`)
  return found
}

describe('seriesPanels', () => {
  test('produces volume plus one panel per rate, in reading order', () => {
    expect(seriesPanels(WEEK, NOW).map((p) => p.key)).toEqual([
      'delivered',
      'bounce_rate',
      'complaint_rate',
      'spam_rate',
    ])
  })

  test('a signal absent on every day is not measured, and says so instead of plotting zero', () => {
    const complaints = panel(WEEK, 'complaint_rate')
    expect(complaints.measured).toBe(false)
    expect(complaints.points.every((p) => p.value === null)).toBe(true)
    expect(complaints.summary).toBe('Not measured in this window.')
    expect(complaints.notMeasured).toContain('No complaint feed is connected')
    expect(complaints.notMeasured).toContain('not a run of clean days')
  })

  test('a signal present on any day is measured, and an absent day stays null', () => {
    const series = [
      day({ date: '2026-08-19', delivered: 100, complained: null }),
      day({ date: '2026-08-20', delivered: 100, complained: 1 }),
    ]
    const complaints = panel(series, 'complaint_rate')
    expect(complaints.measured).toBe(true)
    expect(complaints.points.map((p) => p.value)).toEqual([null, 1])
    expect(complaints.notMeasured).toBeUndefined()
  })

  test('a zero measurement is measured and plots as 0, unlike an absent one', () => {
    const spam = panel(WEEK, 'spam_rate')
    expect(spam.measured).toBe(true)
    expect(spam.points[2]?.value).toBe(0)
  })

  test('a day with nothing delivered has no rate rather than a healthy 0%', () => {
    const series = [day({ date: '2026-08-19', delivered: 0, bounced: 0 }), day({ date: '2026-08-20' })]
    const bounce = panel(series, 'bounce_rate')
    expect(bounce.points[0]?.value).toBeNull()
    expect(bounce.points[1]?.value).toBe(2)
  })

  test('the worst day is named, and rate scales are floored so a clean week is not a spike', () => {
    const bounce = panel(WEEK, 'bounce_rate')
    expect(bounce.summary).toBe('Worst day 9.2% on 19 Aug')
    expect(panel([day({ date: '2026-08-20', delivered: 1000, bounced: 1 })], 'bounce_rate').peak).toBe(1)
  })

  test('the volume panel totals the window and names its peak', () => {
    const delivered = panel(WEEK, 'delivered')
    expect(delivered.measured).toBe(true)
    expect(delivered.peak).toBe(250)
    expect(delivered.summary).toBe('630 delivered in this window · peak 250 in a day')
  })

  test('a window with no delivery at all is not measured rather than a flat zero line', () => {
    const series = [day({ date: '2026-08-19', delivered: 0, bounced: 0 }), day({ date: '2026-08-20', delivered: 0, bounced: 0 })]
    const delivered = panel(series, 'delivered')
    expect(delivered.measured).toBe(false)
    expect(delivered.notMeasured).toContain('No delivery has been recorded')
  })

  test('an empty series yields panels that all read as not measured', () => {
    expect(seriesPanels([], NOW).every((p) => !p.measured)).toBe(true)
  })
})

describe('panelValueLabel', () => {
  test('counts render plainly, rates as percentages, and a missing value as not measured', () => {
    const delivered = panel(WEEK, 'delivered')
    const bounce = panel(WEEK, 'bounce_rate')
    expect(panelValueLabel(delivered, { date: '2026-08-19', value: 1250 })).toBe('1,250')
    expect(panelValueLabel(bounce, { date: '2026-08-19', value: 9.2 })).toBe('9.2%')
    expect(panelValueLabel(bounce, { date: '2026-08-19', value: null })).toBe('Not measured')
  })
})

describe('hasPlottableHistory', () => {
  test('one day is a number, not a trend', () => {
    expect(hasPlottableHistory([])).toBe(false)
    expect(hasPlottableHistory([day({ date: '2026-08-20' })])).toBe(false)
    expect(hasPlottableHistory(WEEK)).toBe(true)
  })
})
