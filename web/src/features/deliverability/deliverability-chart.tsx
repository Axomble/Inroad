import { useState } from 'react'
import { cn } from '@/lib/utils'
import { shortDate } from '@/lib/deliverability-copy'
import type { DeliverabilityPoint } from './api'
import {
  hasPlottableHistory,
  panelValueLabel,
  seriesPanels,
  type PanelPoint,
  type SeriesPanel,
} from './deliverability-series'

/**
 * The per-day series, as small multiples: one panel per signal, each on its own
 * y scale.
 *
 * Deliberately NOT one plot with three lines. Volume and a percentage share no
 * axis (a dual-axis chart invents a correlation that isn't in the data), and
 * separate panels mean colour does no identity work at all — every mark uses the
 * single `--data` hue, which clears 3:1 against the surface in both themes, and
 * the panel title carries the identity instead. That also lets an unmeasured
 * signal render as "not measured" rather than as a flat line along zero.
 *
 * Hand-drawn SVG rather than a chart library: four sparkline-sized panels don't
 * justify the bundle, and this file is lazy-loaded behind Suspense so it isn't in
 * the route's first chunk either. Every plotted value is also in the table view
 * below, so hover never gates a number.
 *
 * Default export so the page can `React.lazy(() => import('./deliverability-chart'))`.
 */
export default function DeliverabilityChart({ series }: { series: DeliverabilityPoint[] }) {
  const panels = seriesPanels(series)

  if (!hasPlottableHistory(series)) {
    return (
      <p className="px-5 py-6 text-sm text-muted-foreground">
        {series.length === 0
          ? 'No days in this window yet. The series starts once this workspace has sent.'
          : 'Only one day of history so far — a single day is a number, not a trend, so nothing is plotted yet.'}
      </p>
    )
  }

  return (
    <div className="space-y-4 px-4 py-4 sm:px-5">
      <div className="grid gap-4 md:grid-cols-2">
        {panels.map((panel) => (
          <Panel key={panel.key} panel={panel} />
        ))}
      </div>
      <SeriesTable panels={panels} />
    </div>
  )
}

const W = 300
const H = 64
/** Head-room so a peak-height mark isn't clipped, and room for the end marker. */
const PAD_T = 5
const PAD_R = 5
const PLOT_H = H - PAD_T
const PLOT_W = W - PAD_R

function Panel({ panel }: { panel: SeriesPanel }) {
  // Index the reader is inspecting, via pointer or arrow keys. `null` = none, in
  // which case the readout shows the panel's own summary.
  const [active, setActive] = useState<number | null>(null)
  const activePoint = active === null ? undefined : panel.points[active]

  const first = panel.points[0]
  const last = panel.points[panel.points.length - 1]

  return (
    <section
      aria-label={panel.title}
      className="rounded-lg border border-border bg-surface px-3.5 py-3 sm:px-4"
    >
      <div className="flex items-baseline justify-between gap-2">
        <h3 className="font-mono text-[10.5px] uppercase tracking-[0.12em] text-faint">{panel.title}</h3>
        {panel.measured && (
          <span className="font-mono text-[10.5px] tabular-nums text-faint">{panel.peakLabel}</span>
        )}
      </div>

      {/* One live readout per panel: the hovered/focused day, or the summary.
          Values are never colour-coded here — this is text, so it wears a text
          token and the mark beside it carries the hue. */}
      <p aria-live="polite" className="mt-0.5 min-h-4 text-[11.5px] text-muted-foreground">
        {activePoint
          ? `${shortDate(activePoint.date)} — ${panelValueLabel(panel, activePoint)}`
          : panel.summary}
      </p>

      {panel.measured ? (
        <Plot panel={panel} active={active} onActive={setActive} />
      ) : (
        // Not measured is a sentence, never an empty plot: a blank axis reads as
        // "zero all week", which is the misreading this whole surface avoids.
        <p className="mt-2 rounded-md bg-surface-2/70 px-2.5 py-3 text-[11.5px] text-muted-foreground">
          <span className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">Not measured</span>{' '}
          — {panel.notMeasured}
        </p>
      )}

      {panel.measured && first && last && (
        <div className="mt-1 flex justify-between font-mono text-[10px] tabular-nums text-faint">
          <span>{shortDate(first.date)}</span>
          <span>{shortDate(last.date)}</span>
        </div>
      )}
    </section>
  )
}

/**
 * The plot itself. Focusable so the same per-day readout is reachable from the
 * keyboard (left/right), which is what keeps the hover layer from being the only
 * way to read a day — the table below is the other.
 */
function Plot({
  panel,
  active,
  onActive,
}: {
  panel: SeriesPanel
  active: number | null
  onActive: (index: number | null) => void
}) {
  const count = panel.points.length

  function moveTo(index: number) {
    onActive(Math.max(0, Math.min(count - 1, index)))
  }

  return (
    // Focusable on purpose: the plot is a data readout, so arrow keys step
    // through days exactly as hover does.
    <div
      tabIndex={0}
      role="group"
      aria-label={`${panel.title} by day. ${panel.summary}. Use the left and right arrow keys to read each day.`}
      className="mt-1.5 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
      onPointerLeave={() => onActive(null)}
      onBlur={() => onActive(null)}
      onPointerMove={(event) => {
        const box = event.currentTarget.getBoundingClientRect()
        if (box.width === 0) return
        const ratio = (event.clientX - box.left) / box.width
        moveTo(Math.round(ratio * (count - 1)))
      }}
      onKeyDown={(event) => {
        if (event.key === 'ArrowRight') {
          event.preventDefault()
          moveTo((active ?? -1) + 1)
        } else if (event.key === 'ArrowLeft') {
          event.preventDefault()
          moveTo((active ?? count) - 1)
        } else if (event.key === 'Escape') {
          onActive(null)
        }
      }}
    >
      <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full" aria-hidden="true" focusable="false">
        {/* Recessive hairline grid: the baseline plus one midline, solid. */}
        <line x1={0} y1={H} x2={W} y2={H} className="stroke-border" strokeWidth={1} />
        <line x1={0} y1={PAD_T + PLOT_H / 2} x2={W} y2={PAD_T + PLOT_H / 2} className="stroke-border" strokeWidth={1} />
        {panel.form === 'count' ? (
          <Columns panel={panel} active={active} />
        ) : (
          <RateLine panel={panel} active={active} />
        )}
      </svg>
    </div>
  )
}

/** y for a value on this panel's own scale; `peak` is floored above 0 upstream. */
function scaleY(panel: SeriesPanel, value: number): number {
  const fraction = panel.peak > 0 ? Math.min(1, value / panel.peak) : 0
  return H - fraction * PLOT_H
}

function Columns({ panel, active }: { panel: SeriesPanel; active: number | null }) {
  const band = W / panel.points.length
  // 2 units of surface between neighbours does the separating — never a stroke
  // around the mark — and a column is capped so a short window keeps its air.
  // The cap is in viewBox units: a panel renders at roughly 1.5× this 300-unit
  // box, so 16 units is the ~24px ceiling a column is allowed on screen.
  const width = Math.min(16, Math.max(1, band - 2))
  return (
    <>
      {panel.points.map((point, index) => {
        if (point.value === null) return null
        const y = scaleY(panel, point.value)
        const height = Math.max(0, H - y)
        const x = band * index + (band - width) / 2
        return (
          <path
            key={point.date}
            d={columnPath(x, y, width, height)}
            className={cn('fill-data', active !== null && active !== index && 'opacity-40')}
          />
        )
      })}
    </>
  )
}

/** A column with a 4px rounded data-end and square corners at the baseline. */
function columnPath(x: number, y: number, width: number, height: number): string {
  const r = Math.min(4, width / 2, height)
  return [
    `M ${x} ${y + height}`,
    `L ${x} ${y + r}`,
    `Q ${x} ${y} ${x + r} ${y}`,
    `L ${x + width - r} ${y}`,
    `Q ${x + width} ${y} ${x + width} ${y + r}`,
    `L ${x + width} ${y + height}`,
    'Z',
  ].join(' ')
}

/**
 * A rate as a 2px line with a 10% area wash. Days with no rate break the line
 * rather than dropping it to zero — a missing divisor is not a clean day.
 */
function RateLine({ panel, active }: { panel: SeriesPanel; active: number | null }) {
  const x = (index: number) => (panel.points.length > 1 ? (index / (panel.points.length - 1)) * PLOT_W : 0)
  const runs = plottableRuns(panel.points)
  const activePoint = active === null ? undefined : panel.points[active]

  return (
    <>
      {runs.map((run) => {
        const first = run[0]
        const last = run[run.length - 1]
        if (!first || !last) return null
        const line = run.map((p) => `${x(p.index).toFixed(2)},${scaleY(panel, p.value).toFixed(2)}`).join(' ')
        return (
          <g key={first.date}>
            {run.length > 1 && (
              <polygon
                points={`${x(first.index).toFixed(2)},${H} ${line} ${x(last.index).toFixed(2)},${H}`}
                className="fill-data/10"
              />
            )}
            <polyline
              points={line}
              fill="none"
              className="stroke-data"
              strokeWidth={2}
              strokeLinecap="round"
              strokeLinejoin="round"
            />
            {/* A single observed day can't be a line, so it gets a marker. */}
            {run.length === 1 && (
              <circle cx={x(first.index)} cy={scaleY(panel, first.value)} r={3} className="fill-data" />
            )}
          </g>
        )
      })}

      {/* End marker with a 2px surface ring so it stays legible over the line. */}
      {endMarker(panel, x)}

      {activePoint?.value != null && (
        <>
          <line
            x1={x(active ?? 0)}
            y1={PAD_T}
            x2={x(active ?? 0)}
            y2={H}
            className="stroke-border-strong"
            strokeWidth={1}
          />
          <circle
            cx={x(active ?? 0)}
            cy={scaleY(panel, activePoint.value)}
            r={4}
            className="fill-data stroke-surface"
            strokeWidth={2}
          />
        </>
      )}
    </>
  )
}

function endMarker(panel: SeriesPanel, x: (index: number) => number) {
  for (let index = panel.points.length - 1; index >= 0; index -= 1) {
    const point = panel.points[index]
    if (point?.value == null) continue
    return (
      <circle
        cx={x(index)}
        cy={scaleY(panel, point.value)}
        r={4}
        className="fill-data stroke-surface"
        strokeWidth={2}
      />
    )
  }
  return null
}

/** Contiguous runs of days that actually have a value, so gaps break the line. */
function plottableRuns(points: PanelPoint[]): { date: string; index: number; value: number }[][] {
  const runs: { date: string; index: number; value: number }[][] = []
  let current: { date: string; index: number; value: number }[] = []
  points.forEach((point, index) => {
    if (point.value === null) {
      if (current.length > 0) runs.push(current)
      current = []
      return
    }
    current.push({ date: point.date, index, value: point.value })
  })
  if (current.length > 0) runs.push(current)
  return runs
}

/**
 * The table twin. Every plotted value is here in text, so nothing is reachable
 * only by hovering a mark — and an unmeasured day reads "Not measured" in the
 * table too, never a zero.
 */
function SeriesTable({ panels }: { panels: SeriesPanel[] }) {
  const dates = panels[0]?.points.map((p) => p.date) ?? []
  return (
    <details className="rounded-lg border border-border bg-surface">
      <summary className="cursor-pointer px-3.5 py-2 font-mono text-[10.5px] uppercase tracking-[0.12em] text-faint">
        Show these days as a table
      </summary>
      <div className="overflow-x-auto px-3.5 pb-3">
        <table className="w-full text-left text-[11.5px] tabular-nums">
          <caption className="sr-only">Deliverability signals per day</caption>
          <thead>
            <tr className="font-mono text-[10px] uppercase tracking-[0.1em] text-faint">
              <th scope="col" className="py-1 pr-3 font-normal">
                Day
              </th>
              {panels.map((panel) => (
                <th key={panel.key} scope="col" className="py-1 pr-3 font-normal">
                  {panel.title}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {dates.map((date, index) => (
              <tr key={date} className="border-t border-border text-muted-foreground">
                <th scope="row" className="py-1 pr-3 font-normal text-foreground">
                  {shortDate(date)}
                </th>
                {panels.map((panel) => {
                  const point = panel.points[index]
                  return (
                    <td key={panel.key} className="py-1 pr-3">
                      {point ? panelValueLabel(panel, point) : 'Not measured'}
                    </td>
                  )
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </details>
  )
}
