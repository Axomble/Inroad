// Turns the API's per-day series into the panels the chart draws. Component-free
// so the rules are unit-tested directly and so the chart file only exports a
// component (fast refresh).
//
// Two rules matter more than the geometry:
//
//  1. A signal with no reported value on any day is NOT MEASURED, and its panel
//     says so instead of drawing a flat line along zero. `complained` and
//     `spam_placed` are nullable in the contract precisely because they can be
//     absent, and a zero line would claim a clean day nobody observed.
//  2. A rate on a day with nothing delivered is `null`, not 0% — the divisor is
//     missing, so there is no rate to plot. The line breaks rather than dipping to
//     a healthy-looking zero.
import { formatPct, shortDate } from '@/lib/deliverability-copy'
import type { DeliverabilityPoint } from '@/store/api'

export type PanelKey = 'delivered' | 'bounce_rate' | 'complaint_rate' | 'spam_rate'

/** One day's plotted value. `null` means there is nothing to plot for that day. */
export interface PanelPoint {
  date: string
  value: number | null
}

export interface SeriesPanel {
  key: PanelKey
  /** Names what is plotted — the panel's only identity channel, since every
   *  panel shares one hue (colour does no identity work here). */
  title: string
  /** `count` draws columns from a zero baseline; `rate` draws a line. */
  form: 'count' | 'rate'
  measured: boolean
  points: PanelPoint[]
  /** Top of this panel's own y scale. Each panel is scaled independently — they
   *  are different measures, so a shared scale would flatten the rates. */
  peak: number
  /** The peak, formatted for the panel's unit. */
  peakLabel: string
  /** One line under the title: the total or the worst day. */
  summary: string
  /** Copy rendered instead of a plot when the signal was never measured. */
  notMeasured?: string
}

const NOT_MEASURED: Record<PanelKey, string> = {
  delivered: 'No delivery has been recorded in this window.',
  bounce_rate: 'Nothing was delivered in this window, so there is no bounce rate to plot.',
  complaint_rate:
    'No complaint feed is connected, so complaints were never measured — this is not a run of clean days.',
  spam_rate:
    'No warmup receipts landed in this window, so spam placement was never observed — this is not a run of clean days.',
}

/** A day's rate as a percentage, or `null` when the divisor is missing. */
function rate(part: number | null | undefined, whole: number): number | null {
  if (part == null || whole <= 0) return null
  return (part / whole) * 100
}

function countPanel(series: DeliverabilityPoint[]): SeriesPanel {
  const points: PanelPoint[] = series.map((d) => ({ date: d.date, value: d.delivered }))
  const total = series.reduce((sum, d) => sum + d.delivered, 0)
  const peak = Math.max(0, ...points.map((p) => p.value ?? 0))
  const measured = series.length > 0 && total > 0
  return {
    key: 'delivered',
    title: 'Delivered per day',
    form: 'count',
    measured,
    points,
    peak,
    peakLabel: peak.toLocaleString(),
    summary: measured
      ? `${total.toLocaleString()} delivered in this window · peak ${peak.toLocaleString()} in a day`
      : 'Nothing delivered in this window.',
    ...(measured ? {} : { notMeasured: NOT_MEASURED.delivered }),
  }
}

function ratePanel(
  key: Exclude<PanelKey, 'delivered'>,
  title: string,
  points: PanelPoint[],
  now: number,
): SeriesPanel {
  const observed = points.filter((p): p is { date: string; value: number } => p.value !== null)
  const measured = observed.length > 0
  const worst = observed.reduce<{ date: string; value: number } | null>(
    (best, p) => (best === null || p.value > best.value ? p : best),
    null,
  )
  // Floor the scale at 1% so a healthy 0.2% day doesn't render as a full-height
  // spike — an autoscaled rate panel makes a clean campaign look alarming.
  const peak = Math.max(1, worst?.value ?? 0)
  return {
    key,
    title,
    form: 'rate',
    measured,
    points,
    peak,
    peakLabel: formatPct(peak),
    summary:
      measured && worst
        ? `Worst day ${formatPct(worst.value)} on ${shortDate(worst.date, now)}`
        : 'Not measured in this window.',
    ...(measured ? {} : { notMeasured: NOT_MEASURED[key] }),
  }
}

/**
 * The panels, in reading order: volume first (it is the sample everything else is
 * a fraction of), then each rate. Rates are per-day and independent, so they are
 * small multiples rather than three lines sharing one plot — which also means no
 * series colour has to carry identity.
 */
export function seriesPanels(series: DeliverabilityPoint[], now: number = Date.now()): SeriesPanel[] {
  return [
    countPanel(series),
    ratePanel(
      'bounce_rate',
      'Bounce rate',
      series.map((d) => ({ date: d.date, value: rate(d.bounced, d.delivered) })),
      now,
    ),
    ratePanel(
      'complaint_rate',
      'Complaint rate',
      series.map((d) => ({ date: d.date, value: rate(d.complained, d.delivered) })),
      now,
    ),
    ratePanel(
      'spam_rate',
      'Spam placement',
      series.map((d) => ({ date: d.date, value: rate(d.spam_placed, d.delivered) })),
      now,
    ),
  ]
}

/** A panel's value for one day, formatted for its unit — used by hover and the table. */
export function panelValueLabel(panel: SeriesPanel, point: PanelPoint): string {
  if (point.value === null) return 'Not measured'
  return panel.form === 'count' ? point.value.toLocaleString() : formatPct(point.value)
}

/**
 * Two points is the minimum a line can be drawn from; a single day is a number,
 * not a trend, so the chart declines to draw rather than plotting one dot and
 * implying a shape.
 */
export function hasPlottableHistory(series: DeliverabilityPoint[]): boolean {
  return series.length >= 2
}
