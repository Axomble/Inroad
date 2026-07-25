import { useEffect, useState } from 'react'
import { EmptyBlock, SectionBar } from '@/components/layout/page'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { httpStatus } from '@/lib/rtk-error'
import { ReplyClassPill } from './reply-class-pill'
import { useListCampaignEnrollmentsQuery, type CampaignEnrollment } from './api'

/**
 * A short, human date for an enrollment's reply timestamp, or an em-dash when
 * the contact hasn't replied (or the timestamp is unparseable). Kept
 * locale-aware but stable — no time-of-day noise in a dense list.
 */
function formatRepliedAt(iso: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}

/**
 * CampaignEnrollmentsList lists a campaign's enrolled contacts with their
 * classified reply. Owns its own loading / empty / error states so the parent
 * can mount it unconditionally inside the campaign detail view.
 */
export function CampaignEnrollmentsList({ campaignId }: { campaignId: string }) {
  const [offset, setOffset] = useState(0)
  const pageSize = 50
  // Look-ahead: request one extra row so we can distinguish "exactly a full
  // page" from "there's a next page" without a separate count endpoint (mirrors
  // the contacts list). The extra row is trimmed off before render.
  const { data, isLoading, error } = useListCampaignEnrollmentsQuery(
    { id: campaignId, limit: pageSize + 1, offset },
    // Replies are classified server-side by the worker; no client mutation can
    // invalidate a cache tag for that (see api.ts). Refetch on mount/arg-change
    // so reopening the detail — or paging — pulls fresh reply classes. This is
    // the deliberate substitute for the dropped (dead) Enrollment tag; no
    // continuous polling.
    { refetchOnMountOrArgChange: true },
  )
  const fetched = data ?? []
  const hasMore = fetched.length > pageSize
  const enrollments = hasMore ? fetched.slice(0, pageSize) : fetched

  // Reset to the first page when the selected campaign changes (the parent
  // reuses this component's slot rather than remounting per campaign).
  useEffect(() => {
    setOffset(0)
  }, [campaignId])

  return (
    <div className="border-b border-border bg-surface/40">
      <SectionBar label="Contacts" count={enrollments.length} />

      {isLoading ? (
        <LoadingRows />
      ) : error ? (
        <div role="alert" className="px-5 py-6 text-sm text-danger">
          Couldn't load contacts{httpStatus(error) ? ` (${httpStatus(error)})` : ''} — try again.
        </div>
      ) : enrollments.length === 0 ? (
        <EmptyBlock
          title="No enrollments yet"
          description="Contacts appear here once the campaign is launched and starts enrolling its target list."
        />
      ) : (
        <ul>
          {enrollments.map((enrollment) => (
            <EnrollmentRow key={enrollment.email} enrollment={enrollment} />
          ))}
        </ul>
      )}

      {/* Pager: only meaningful once there's a page to move to. Hidden on the
          first page with no next page to avoid dead controls on short lists. */}
      {(offset > 0 || hasMore) && (
        <div className="flex items-center justify-end gap-2 border-t border-border px-5 py-2">
          <Button
            variant="ghost"
            size="sm"
            disabled={offset === 0 || isLoading}
            onClick={() => setOffset((o) => Math.max(0, o - pageSize))}
          >
            Previous
          </Button>
          <Button
            variant="ghost"
            size="sm"
            disabled={!hasMore || isLoading}
            onClick={() => setOffset((o) => o + pageSize)}
          >
            Next
          </Button>
        </div>
      )}
    </div>
  )
}

function EnrollmentRow({ enrollment }: { enrollment: CampaignEnrollment }) {
  return (
    <li className="flex items-center gap-4 border-b border-border px-5 py-3 last:border-b-0">
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13.5px] text-foreground">{enrollment.first_name || '—'}</div>
        <div className="truncate font-mono text-[11px] text-faint">{enrollment.email}</div>
      </div>

      <span className="w-24 shrink-0 truncate font-mono text-[10.5px] uppercase tracking-[0.12em] text-muted-foreground">
        {enrollment.status}
      </span>

      {/* ReplyClassPill renders nothing when there's no recognized class, so the
          column simply stays empty for un-replied contacts. */}
      <div className="w-32 shrink-0">
        <ReplyClassPill replyClass={enrollment.reply_class} />
      </div>

      <span className="w-24 shrink-0 text-right font-mono text-[11px] tabular-nums text-faint">
        {formatRepliedAt(enrollment.replied_at)}
      </span>
    </li>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1, 2].map((i) => (
        <li key={i} className="flex items-center gap-4 border-b border-border px-5 py-3.5 last:border-b-0">
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-32" />
            <Skeleton className="h-2.5 w-48" />
          </div>
          <Skeleton className="h-3 w-16" />
          <Skeleton className="h-3 w-24" />
          <Skeleton className="h-3 w-16" />
        </li>
      ))}
    </ul>
  )
}
