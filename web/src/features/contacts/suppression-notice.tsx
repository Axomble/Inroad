import { Ban, TriangleAlert } from 'lucide-react'
import { formatDateTime } from '@/lib/datetime'
import { cn } from '@/lib/utils'
import type { ContactSuppression } from './api'

/**
 * Whether this person may be emailed — the first question a contact record has to
 * answer, above every engagement metric.
 *
 * Two genuinely different situations, and they must not read the same:
 * - `is_primary_email` — the address the send path resolves is suppressed, so
 *   this contact cannot be emailed at all. A hard stop.
 * - otherwise — only an old alias is suppressed. Sending works today, but
 *   promoting that alias would silently stop it. A warning, not a block.
 *
 * The state is carried by a heading, an icon and a sentence. Colour agrees with
 * all three but is never the thing that says it.
 */
export function SuppressionNotice({ suppression }: { suppression: ContactSuppression }) {
  const blocked = suppression.is_primary_email
  const Icon = blocked ? Ban : TriangleAlert

  return (
    <section
      aria-labelledby="suppression-heading"
      className={cn(
        'flex min-w-0 items-start gap-3 border-b p-4 sm:px-5',
        blocked ? 'border-danger/30 bg-danger/10' : 'border-warn/30 bg-warn/10',
      )}
    >
      <Icon className={cn('mt-0.5 size-5 shrink-0', blocked ? 'text-danger' : 'text-warn')} aria-hidden="true" />
      <div className="min-w-0">
        <h2
          id="suppression-heading"
          className={cn('text-sm font-semibold', blocked ? 'text-danger' : 'text-warn')}
        >
          {blocked ? 'Do not email this contact' : 'One of this contact’s addresses is suppressed'}
        </h2>
        <p className="mt-1 text-sm text-foreground">
          {reasonSentence(suppression.reason)}{' '}
          <span className="font-medium break-all">{suppression.email}</span> was suppressed on{' '}
          <time dateTime={suppression.since}>{formatDateTime(suppression.since)}</time>.
        </p>
        <p className="mt-1 text-xs text-muted-foreground">
          {blocked
            ? 'This is the address sending resolves, so campaigns will skip this contact.'
            : 'Their primary address is still deliverable, so campaigns continue — but making this address primary would stop them.'}
        </p>
      </div>
    </section>
  )
}

/**
 * The reason in words. `complaint` is never collapsed into `unsubscribe`: being
 * reported as spam and being asked to stop are very different things for the
 * person reading this to know.
 */
function reasonSentence(reason: ContactSuppression['reason']): string {
  switch (reason) {
    case 'unsubscribe':
      return 'They asked to stop receiving email.'
    case 'bounce':
      return 'Mail to them bounced.'
    case 'complaint':
      return 'They reported a message as spam.'
    case 'manual':
      return 'Someone in this workspace suppressed them by hand.'
  }
}
