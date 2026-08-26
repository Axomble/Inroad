import { AlertCircle } from 'lucide-react'
// Cross-feature READ-ONLY query hook. Permitted by CLAUDE.md ("a feature may
// import read-only RTK Query hooks from another feature's api.ts — hooks only,
// never components/state"); the panel below renders its own UI rather than
// importing contacts' EngagementPanel, which is a full-page section at the wrong
// density for a rail.
import { useGetContactEngagementQuery, type ContactEngagement } from '@/features/contacts/api'
import { InlineLoading, MutedEmpty } from '@/components/shared/record-page'
import { recordErrorMessage } from '@/features/records/error-copy'
import { relativeTime } from '@/lib/relative-time'

/**
 * The contact's engagement at a glance, for the reader's context rail.
 *
 * Deliberately narrower than `contacts/engagement-panel.tsx`: that one is a
 * four-up metric grid plus a campaign list, sized for a record page. Here the
 * question is only "is this person warm?", answered in one line of counters.
 */
export function ContactEngagementStrip({ contactId }: { contactId: string }) {
  const { data, isLoading, error } = useGetContactEngagementQuery({ id: contactId })

  if (isLoading) return <InlineLoading label="Loading engagement" />
  if (error !== undefined) {
    return (
      <p role="status" className="text-[11px] text-warn">
        {recordErrorMessage(error, "Engagement couldn't be loaded.")}
      </p>
    )
  }
  if (!data) return <MutedEmpty text="No engagement recorded." />
  if (data.emails_sent === 0) return <MutedEmpty text="Nothing sent to this contact yet." />

  return (
    <div className="space-y-1.5">
      <dl className="grid grid-cols-4 gap-1.5">
        <Metric label="Sent" value={data.emails_sent} />
        <Metric label="Opens" value={data.opens_indicative} />
        <Metric label="Clicks" value={data.clicks} />
        <Metric label="Replies" value={data.replies} />
      </dl>

      {/* The honest caveat: with tracking off, zero opens is an absence of
          measurement, not an absence of interest. Derived from the API's own
          `opens_measurable` flag rather than from `campaigns[].tracking_enabled`,
          which is capped at 20 and so cannot answer this. */}
      {opensUnmeasured(data) && (
        <p className="flex items-start gap-1 text-[10px] text-faint">
          <AlertCircle className="mt-px size-2.5 shrink-0" aria-hidden="true" />
          <span>Open tracking is off for this contact's campaigns — opens are not measurable.</span>
        </p>
      )}

      {(data.bounces > 0 || data.unsubscribes > 0) && (
        <p className="text-[10px] text-warn">
          {data.bounces > 0 && `${data.bounces} bounced`}
          {data.bounces > 0 && data.unsubscribes > 0 && ' · '}
          {data.unsubscribes > 0 && `${data.unsubscribes} unsubscribed`}
        </p>
      )}

      {data.last_activity_at && (
        <p className="text-[10px] text-faint">Last activity {relativeTime(data.last_activity_at)}</p>
      )}
    </div>
  )
}

/**
 * Whether a zero open-count means "not opened" or "not measured". Mail was sent
 * but no campaign involved had open tracking on, so the counter is silent rather
 * than negative.
 */
function opensUnmeasured(data: ContactEngagement): boolean {
  return data.emails_sent > 0 && !data.opens_measurable
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="min-w-0">
      <dt className="truncate font-mono text-[9px] tracking-wide text-faint uppercase">{label}</dt>
      <dd className="font-mono text-[13px] tabular-nums text-foreground">{value}</dd>
    </div>
  )
}
