import { fireEvent, screen, within } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { render } from '@testing-library/react'
import type { DeliverabilityPoint } from '@/store/api'
import DeliverabilityChart from './deliverability-chart'

const SERIES: DeliverabilityPoint[] = [
  { date: '2026-08-18', delivered: 200, bounced: 4, complained: null, spam_placed: 6 },
  { date: '2026-08-19', delivered: 250, bounced: 23, complained: null, spam_placed: 5 },
  { date: '2026-08-20', delivered: 180, bounced: 3, complained: null, spam_placed: 0 },
]

/** No store or router needed — the chart is a pure view over its props. */
const renderChart = (series = SERIES) => render(<DeliverabilityChart series={series} />)

describe('DeliverabilityChart', () => {
  test('each measured signal gets its own titled panel on its own scale', () => {
    renderChart()
    expect(within(screen.getByRole('region', { name: 'Delivered per day' })).getByText('250')).toBeInTheDocument()
    // The bounce panel's peak is its own worst rate, not the volume peak.
    expect(within(screen.getByRole('region', { name: 'Bounce rate' })).getByText('9.2%')).toBeInTheDocument()
  })

  test('arrow keys read out a day, so the value is not hover-only', () => {
    renderChart()
    const panel = screen.getByRole('region', { name: 'Bounce rate' })
    const plot = within(panel).getByRole('group', { name: /Bounce rate by day/ })

    fireEvent.keyDown(plot, { key: 'ArrowRight' })
    expect(within(panel).getByText('18 Aug — 2.0%')).toBeInTheDocument()

    fireEvent.keyDown(plot, { key: 'ArrowRight' })
    expect(within(panel).getByText('19 Aug — 9.2%')).toBeInTheDocument()

    // Off the end it holds at the last day rather than wrapping or clearing.
    fireEvent.keyDown(plot, { key: 'ArrowRight' })
    fireEvent.keyDown(plot, { key: 'ArrowRight' })
    expect(within(panel).getByText('20 Aug — 1.7%')).toBeInTheDocument()

    // Blur returns the panel to its own summary.
    fireEvent.blur(plot)
    expect(within(panel).getByText('Worst day 9.2% on 19 Aug')).toBeInTheDocument()
  })

  test('a day with no value reads as not measured in the readout, never as 0%', () => {
    renderChart([
      { date: '2026-08-19', delivered: 0, bounced: 0, complained: null, spam_placed: null },
      { date: '2026-08-20', delivered: 100, bounced: 5, complained: null, spam_placed: null },
    ])
    const panel = screen.getByRole('region', { name: 'Bounce rate' })
    const plot = within(panel).getByRole('group', { name: /Bounce rate by day/ })

    fireEvent.keyDown(plot, { key: 'ArrowRight' })
    expect(within(panel).getByText('19 Aug — Not measured')).toBeInTheDocument()
  })

  test('an unmeasured signal has no plot to misread as a flat zero line', () => {
    renderChart()
    const complaints = screen.getByRole('region', { name: 'Complaint rate' })
    expect(complaints.querySelector('svg')).toBeNull()
    expect(within(complaints).getByText('Not measured')).toBeInTheDocument()
    // …while a measured one does plot.
    expect(screen.getByRole('region', { name: 'Bounce rate' }).querySelector('svg')).not.toBeNull()
  })

  test('an empty window says the series has not started rather than drawing axes', () => {
    renderChart([])
    expect(screen.getByText(/The series starts once this workspace has sent/)).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Bounce rate' })).not.toBeInTheDocument()
  })
})
