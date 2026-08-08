/**
 * Absolute date and time formatting, in the reader's own locale and timezone.
 *
 * `Intl.DateTimeFormat` is the whole implementation on purpose. A date library
 * would have us commit to a pattern string — and so to one region's field order
 * and one clock — where `Intl` reads the browser's own locale and zone for free.
 * There is no calendar *arithmetic* in this app to justify a dependency either;
 * scheduling math lives in the Go backend.
 *
 * Formatters are built once, at module scope. Constructing an
 * `Intl.DateTimeFormat` is the expensive part — far more than formatting with one
 * — so a `new Intl.DateTimeFormat(...)` inside a render path is a per-row cost
 * for no benefit.
 *
 * Each export is named for what it is *for*, not for its options. If a screen
 * genuinely needs a different shape, add a named export for that intent rather
 * than threading options through a call site. Relative phrasing ("3 hours ago")
 * is a different job and lives in `@/lib/relative-time`.
 */

/** Anything an API timestamp arrives as. */
type Instant = string | number | Date

export interface DateTimeFormatters {
  /** "12 Aug 2026" — a day, no clock. */
  date: (value: Instant) => string
  /** "12 Aug 2026, 14:30" — the default for a record's timestamps. */
  dateTime: (value: Instant) => string
  /** "12 Aug, 14:30" — recent activity, where the year is noise. */
  shortDateTime: (value: Instant) => string
  /** "14:30" — a clock time in the reader's own hour cycle. */
  time: (value: Instant) => string
  /**
   * "14:30" — a clock time forced to 24 hours. For dense chrome where "02:30 PM"
   * would not fit and the extra characters buy nothing.
   */
  clock24: (value: Instant) => string
  /** "August 2026" — a heading that groups rows by month. */
  monthYear: (value: Instant) => string
}

/**
 * Builds a set of formatters. The app uses the defaults below; the parameters
 * exist so tests can pin a locale and timezone — an `Intl` assertion that leans
 * on the runner's environment passes on one machine and fails on the next.
 */
export function createDateTimeFormatters(
  locale?: string | readonly string[],
  timeZone?: string,
): DateTimeFormatters {
  const build = (options: Intl.DateTimeFormatOptions) => {
    const formatter = new Intl.DateTimeFormat(locale, timeZone ? { ...options, timeZone } : options)
    return (value: Instant) => formatter.format(new Date(value))
  }
  return {
    date: build({ dateStyle: 'medium' }),
    dateTime: build({ dateStyle: 'medium', timeStyle: 'short' }),
    shortDateTime: build({ month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }),
    time: build({ hour: '2-digit', minute: '2-digit' }),
    clock24: build({ hour: '2-digit', minute: '2-digit', hour12: false }),
    monthYear: build({ month: 'long', year: 'numeric' }),
  }
}

const formatters = createDateTimeFormatters()

export const formatDate = formatters.date
export const formatDateTime = formatters.dateTime
export const formatShortDateTime = formatters.shortDateTime
export const formatTime = formatters.time
export const formatClock24 = formatters.clock24
export const formatMonthYear = formatters.monthYear
