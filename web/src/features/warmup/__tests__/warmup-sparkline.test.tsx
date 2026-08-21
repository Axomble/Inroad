import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'
import type { WarmupDayStat } from '@/store/api'
import WarmupSparkline from '../warmup-sparkline'

/** A zero-activity day; override the fields a case cares about. */
function day(overrides: Partial<WarmupDayStat> = {}): WarmupDayStat {
  return { day: '2026-07-01', sent: 0, received: 0, inbox: 0, spam: 0, replies: 0, ...overrides }
}

test('an empty series renders the not-enough-history fallback, not a chart', () => {
  const { container } = render(<WarmupSparkline series={[]} />)

  expect(screen.getByText(/not enough history yet/i)).toBeInTheDocument()
  expect(container.querySelector('svg')).toBeNull()
})

test('a single data point is treated as insufficient history (no /0 in scaling)', () => {
  const { container } = render(<WarmupSparkline series={[day({ sent: 5, received: 3 })]} />)

  expect(screen.getByText(/not enough history yet/i)).toBeInTheDocument()
  expect(container.querySelector('svg')).toBeNull()
})

test('an all-zero series renders finite path coordinates and the legend', () => {
  const series = [day(), day({ day: '2026-07-02' }), day({ day: '2026-07-03' })]
  const { container } = render(<WarmupSparkline series={series} />)

  const polylines = container.querySelectorAll('polyline')
  expect(polylines).toHaveLength(2)
  // The peak floors at 1, so an all-zero series must not divide by zero and must
  // never emit NaN coordinates into the SVG path.
  for (const line of polylines) {
    const points = line.getAttribute('points') ?? ''
    expect(points).not.toMatch(/NaN/)
    expect(points.length).toBeGreaterThan(0)
  }
  // Colour isn't the only signal — the text legend stays present.
  expect(screen.getByText(/^sent$/i)).toBeInTheDocument()
  expect(screen.getByText(/^received$/i)).toBeInTheDocument()
})
