const UNITS: readonly [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 31_536_000_000],
  ['month', 2_592_000_000],
  ['day', 86_400_000],
  ['hour', 3_600_000],
  ['minute', 60_000],
]

/**
 * Relative phrasing for an ISO timestamp, e.g. "in 5 days" / "3 hours ago".
 * `now` is injectable so tests are deterministic.
 *
 * Lives in `lib` rather than a feature: the auth session/API-key screens and the
 * campaign sender pool all render "last used"-style timestamps, and features may
 * not import each other.
 */
export function relativeTime(iso: string, now: number = Date.now()): string {
  const diffMs = new Date(iso).getTime() - now
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
  const abs = Math.abs(diffMs)
  for (const [unit, ms] of UNITS) {
    if (abs >= ms) return rtf.format(Math.round(diffMs / ms), unit)
  }
  return rtf.format(Math.round(diffMs / 1000), 'second')
}
