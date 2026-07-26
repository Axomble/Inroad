import type { WarmupDayStat } from '@/store/api'

/**
 * A compact 30-day sent/received sparkline. Two overlaid polylines in one
 * normalized SVG viewBox — no chart library, so it stays a few hundred bytes
 * and is lazy-loaded behind Suspense by the card (honoring the route/chart
 * code-split rule). Colors come from the shared chart tokens (dark-mode aware);
 * a text legend accompanies them so series are distinguishable without color.
 *
 * Default export so it can be `React.lazy(() => import('./warmup-sparkline'))`.
 */
export default function WarmupSparkline({ series }: { series: WarmupDayStat[] }) {
  if (series.length < 2) {
    return (
      <p className="font-mono text-[10.5px] uppercase tracking-[0.1em] text-faint">
        Not enough history yet
      </p>
    )
  }

  const W = 100
  const H = 28
  // Shared scale so the two series are comparable; floor at 1 to avoid /0.
  const peak = Math.max(1, ...series.flatMap((d) => [d.sent, d.received]))
  const line = (pick: (d: WarmupDayStat) => number) =>
    series
      .map((d, i) => {
        const x = (i / (series.length - 1)) * W
        const y = H - (pick(d) / peak) * H
        return `${x.toFixed(2)},${y.toFixed(2)}`
      })
      .join(' ')

  return (
    <div className="flex flex-col gap-1">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        preserveAspectRatio="none"
        className="h-8 w-full"
        role="img"
        aria-label={`30-day warmup volume: peak ${peak} emails in a day`}
      >
        <polyline points={line((d) => d.received)} fill="none" className="stroke-chart-2" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
        <polyline points={line((d) => d.sent)} fill="none" className="stroke-chart-4" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
      </svg>
      <div className="flex items-center gap-3 font-mono text-[10px] uppercase tracking-[0.1em] text-faint">
        <LegendKey className="bg-chart-4" label="Sent" />
        <LegendKey className="bg-chart-2" label="Received" />
      </div>
    </div>
  )
}

function LegendKey({ className, label }: { className: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <span className={`h-0.5 w-3 rounded-full ${className}`} aria-hidden="true" />
      {label}
    </span>
  )
}
