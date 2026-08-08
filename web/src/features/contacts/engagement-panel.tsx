import { Link } from '@tanstack/react-router'
import { Skeleton } from '@/components/ui/skeleton'
import { recordErrorMessage } from '@/features/records/error-copy'
import { formatDateTime } from '@/lib/datetime'
import { QueryErrorBanner, Section } from '@/components/shared/record-page'

import { useGetContactEngagementQuery, type ContactCampaignEnrollment, type ContactEngagement } from './api'

/**
 * What this contact has actually done with our mail, over their whole lifetime.
 *
 * This is the panel a general-purpose CRM structurally cannot show, so it sits at
 * the top of the record. It is also the expensive half of the page — four
 * aggregate queries against the detail read's two index seeks — which is why it
 * owns its own request and its own loading state instead of holding the header
 * back.
 *
 * Lifetime totals, deliberately: no window control. A rolling 30 days would
 * quietly hide the whole relationship, which is the one thing a record page is
 * for.
 */
export function EngagementPanel({ contactId }: { contactId: string }) {
  const query = useGetContactEngagementQuery({ id: contactId })

  return (
    <Section title="Email engagement" description="Everything this contact has been sent, and what came back.">
      {query.isLoading ? (
        <div className="grid gap-3 sm:grid-cols-4" aria-label="Loading engagement">
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      ) : null}
      {query.isError ? (
        <QueryErrorBanner
          className=""
          message={recordErrorMessage(query.error, "This contact's engagement could not be loaded.")}
          onRetry={() => void query.refetch()}
          retrying={query.isFetching}
        />
      ) : null}
      {query.data ? <Engagement engagement={query.data} /> : null}
    </Section>
  )
}

function Engagement({ engagement }: { engagement: ContactEngagement }) {
  const sentAnything = engagement.emails_sent > 0
  const unmeasured = opensUnmeasured(engagement)

  return (
    <>
      <dl className="grid gap-3 sm:grid-cols-4">
        <Metric label="Sent" value={engagement.emails_sent} />
        {/* Named "indicative" in the contract for a reason, and the hedge has to
            be visible here rather than only in the type: image proxies prefetch
            mail, so an open is a hint and a click is evidence. */}
        <Metric
          label="Opens (indicative)"
          value={unmeasured && engagement.opens_indicative === 0 ? 'Not measured' : engagement.opens_indicative}
          sub={sentAnything && !unmeasured ? `${percent(engagement.open_rate)} of sent` : undefined}
        />
        <Metric
          label="Clicks"
          value={unmeasured && engagement.clicks === 0 ? 'Not measured' : engagement.clicks}
          sub={sentAnything && !unmeasured ? `${percent(engagement.click_rate)} of sent` : undefined}
        />
        <Metric label="Replies" value={engagement.replies} />
      </dl>
      {unmeasured ? (
        // Present tense, deliberately. The flag reflects each campaign's *current*
        // tracking setting — nothing records what it was at send time — so
        // "tracking is off" is defensible where "this was never measured" would be
        // a claim about the past that a later toggle could falsify.
        <p className="mt-2 text-xs text-muted-foreground">
          Open and click tracking is off for this contact's campaigns, so there is nothing to measure here — these are
          not zeroes. Replies are counted either way.
        </p>
      ) : (
        <p className="mt-2 text-xs text-muted-foreground">
          Opens are approximate — mail providers prefetch images, which registers as an open nobody made. Clicks and
          replies are the reliable signals.
        </p>
      )}
      <dl className="mt-4 grid gap-3 sm:grid-cols-3">
        <Metric label="Bounced" value={engagement.bounces} />
        <Metric label="Unsubscribed" value={engagement.unsubscribes} />
        <Metric
          label="Last activity"
          // Never sent to and never heard from are the same absence, and it is a
          // fact rather than a missing value.
          value={engagement.last_activity_at ? formatDateTime(engagement.last_activity_at) : 'Nothing yet'}
        />
      </dl>
      <Enrollments
        campaigns={engagement.campaigns}
        truncated={engagement.campaigns_truncated}
        total={engagement.campaigns_enrolled}
      />
    </>
  )
}

function Enrollments({
  campaigns,
  truncated,
  total,
}: {
  campaigns: ContactCampaignEnrollment[]
  truncated: boolean
  total: number
}) {
  return (
    <div className="mt-5 border-t border-border pt-4">
      <h3 className="text-sm font-semibold">Campaigns</h3>
      {campaigns.length === 0 ? (
        <p className="mt-2 text-sm text-muted-foreground">This contact has never been enrolled in a campaign.</p>
      ) : (
        <ul className="mt-2 space-y-2">
          {campaigns.map((enrollment) => <Enrollment key={enrollment.campaign_id} enrollment={enrollment} />)}
        </ul>
      )}
      {/* A list silently cut short is how a reader concludes data is missing. */}
      {truncated ? (
        <p role="status" className="mt-2 text-xs text-muted-foreground">
          Showing the {campaigns.length} most recent of {total} enrolments.
        </p>
      ) : null}
    </div>
  )
}

function Enrollment({ enrollment }: { enrollment: ContactCampaignEnrollment }) {
  return (
    <li className="rounded-lg border border-border bg-background p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <Link
          to="/app/campaigns/$id"
          params={{ id: enrollment.campaign_id }}
          className="text-sm font-medium text-foreground underline-offset-2 hover:text-accent-ink hover:underline"
        >
          {enrollment.campaign_name}
        </Link>
        <span className="text-xs font-medium text-muted-foreground">{statusLabel(enrollment.status)}</span>
      </div>
      {/* Which campaign had tracking off is the detail someone drills into after
          the summary above tells them the opens look wrong. */}
      {!enrollment.tracking_enabled ? (
        <p className="mt-1 text-xs text-muted-foreground">No open or click tracking on this campaign.</p>
      ) : null}
      <p className="mt-1 text-xs text-muted-foreground">
        {enrollment.current_step === 0 ? 'Enrolled, not yet sent to' : `Step ${enrollment.current_step} was the last sent`}
        {' · '}
        <time dateTime={enrollment.enrolled_at}>Enrolled {formatDateTime(enrollment.enrolled_at)}</time>
        {enrollment.last_sent_at ? (
          <>
            {' · '}
            <time dateTime={enrollment.last_sent_at}>Last sent {formatDateTime(enrollment.last_sent_at)}</time>
          </>
        ) : null}
      </p>
      {/* Why the sequence stopped is usually the question that brought someone
          to this record in the first place. */}
      {enrollment.stop_reason ? (
        <p className="mt-1 text-xs font-medium text-foreground">Stopped: {stopReasonLabel(enrollment.stop_reason)}</p>
      ) : null}
    </li>
  )
}

function Metric({ label, value, sub }: { label: string; value: React.ReactNode; sub?: string }) {
  return (
    <div className="rounded-lg border border-border bg-background p-3">
      <dt className="font-mono text-[10px] uppercase tracking-[0.12em] text-faint">{label}</dt>
      <dd className="mt-1 text-lg font-light tabular-nums text-foreground">{value}</dd>
      {sub ? <dd className="mt-0.5 font-mono text-[11px] text-muted-foreground">{sub}</dd> : null}
    </div>
  )
}

function statusLabel(status: ContactCampaignEnrollment['status']): string {
  switch (status) {
    case 'active':
      return 'In progress'
    case 'completed':
      return 'Finished the sequence'
    case 'stopped':
      return 'Stopped early'
  }
}

/**
 * The API documents replied / bounced / suppressed / manual, but the field is an
 * open string, so an unknown reason is shown as-is rather than swallowed.
 */
function stopReasonLabel(reason: string): string {
  switch (reason) {
    case 'replied':
      return 'they replied'
    case 'bounced':
      return 'mail to them bounced'
    case 'suppressed':
      return 'their address was suppressed'
    case 'manual':
      return 'stopped by hand'
    case 'failed':
      return 'sending failed'
    default:
      return reason
  }
}

/** The API sends 0..1 fractions, never percentages, and never NaN. */
function percent(rate: number): string {
  return `${(Math.round(rate * 1000) / 10).toLocaleString()}%`
}

/**
 * True when a zero in opens or clicks means "nothing to measure" rather than
 * "nobody did it". A campaign with tracking off still contributes to
 * `emails_sent`, but cannot contribute an open or a click.
 *
 * This reads the server's `opens_measurable`, which is computed over the whole
 * send history. It must NOT be derived from `campaigns[].tracking_enabled`: that
 * list is capped at 20 newest-first, so for a contact whose newest enrolments are
 * untracked and whose older ones were tracked, a client-side `some()` answers
 * false and explains away a genuine zero — a wrong hedge is worse than an
 * uninformative one.
 */
function opensUnmeasured(engagement: ContactEngagement): boolean {
  return engagement.emails_sent > 0 && !engagement.opens_measurable
}
