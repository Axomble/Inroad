// Grouping the thread list into the time buckets a mail client shows
// ("Today", "Yesterday", …). Pure and component-free, so the boundary rules
// are unit-tested directly rather than through a rendered list — the same
// split inbox-search.ts already uses.

/** Bucket keys, in the order they appear in the list. */
export const THREAD_BUCKETS = ['today', 'yesterday', 'this_week', 'this_month', 'earlier'] as const

export type ThreadBucket = (typeof THREAD_BUCKETS)[number]

export const BUCKET_LABELS: Record<ThreadBucket, string> = {
  today: 'Today',
  yesterday: 'Yesterday',
  this_week: 'Earlier this week',
  this_month: 'Earlier this month',
  earlier: 'Older',
}

/**
 * Which bucket a timestamp falls in, relative to `now`.
 *
 * `now` is a required parameter rather than read from the clock inside, so the
 * boundary rules are testable at a fixed instant and every row in one render
 * is bucketed against the same instant (a list bucketed across a midnight tick
 * mid-render would show two "Today" groups).
 *
 * All comparisons are in the viewer's LOCAL zone, because these labels are
 * claims about the viewer's day — matching the `tz_offset` the scope counts are
 * computed with.
 *
 * A timestamp in the future (clock skew between the server and the viewer, or
 * a scheduled send) buckets as `today` rather than falling through to
 * `earlier`: it is the freshest thing in the list, and showing it under "Older"
 * would bury it.
 */
export function bucketFor(occurredAt: Date, now: Date): ThreadBucket {
  const startOfToday = startOfDay(now)
  if (occurredAt >= startOfToday) return 'today'

  const startOfYesterday = addDays(startOfToday, -1)
  if (occurredAt >= startOfYesterday) return 'yesterday'

  // Monday-based, matching the API's `this_week` scope and ISO-8601.
  const startOfWeek = addDays(startOfToday, -daysSinceMonday(startOfToday))
  if (occurredAt >= startOfWeek) return 'this_week'

  const startOfMonth = new Date(now.getFullYear(), now.getMonth(), 1)
  if (occurredAt >= startOfMonth) return 'this_month'

  return 'earlier'
}

function startOfDay(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate())
}

function addDays(d: Date, days: number): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + days)
}

/** `getDay()` puts Sunday at 0, so Sunday is 6 days into a Monday-based week. */
function daysSinceMonday(d: Date): number {
  return (d.getDay() + 6) % 7
}

/** One bucket's worth of items, in the order they arrived. */
export interface BucketGroup<T> {
  bucket: ThreadBucket
  label: string
  items: T[]
}

/**
 * Splits an already-sorted (newest-first) list into its time buckets,
 * preserving order within each and omitting empty ones.
 *
 * Takes a `timeOf` accessor rather than a fixed field so this stays independent
 * of the thread shape. An unparseable timestamp buckets as `earlier` rather
 * than throwing: one malformed row must not blank the whole list.
 */
export function groupByBucket<T>(items: readonly T[], timeOf: (item: T) => string, now: Date): BucketGroup<T>[] {
  const byBucket = new Map<ThreadBucket, T[]>()
  for (const item of items) {
    const at = new Date(timeOf(item))
    const bucket = Number.isNaN(at.getTime()) ? 'earlier' : bucketFor(at, now)
    const existing = byBucket.get(bucket)
    if (existing) existing.push(item)
    else byBucket.set(bucket, [item])
  }
  // Driven by THREAD_BUCKETS, not the Map's insertion order, so the groups are
  // always chronological even if the input isn't perfectly sorted.
  return THREAD_BUCKETS.flatMap((bucket) => {
    const bucketItems = byBucket.get(bucket)
    return bucketItems ? [{ bucket, label: BUCKET_LABELS[bucket], items: bucketItems }] : []
  })
}
