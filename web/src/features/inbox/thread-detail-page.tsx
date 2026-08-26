import { Link, getRouteApi } from '@tanstack/react-router'
import { ArrowLeft } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ReplyClassPill } from '@/components/shared/reply-class-pill'
import { NotFound } from '@/components/shared/not-found'
import { Page, PageBody, PageTopbar } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { useGetInboxThreadQuery } from './api'
import { contactLabel } from './contact-label'
import { ThreadReader } from './thread-reader'
import { SnoozeMenu } from './snooze-menu'
import { LabelPicker } from './label-picker'

const routeApi = getRouteApi('/app/inbox/$threadId')

/**
 * One thread at its own address — a real, linkable page, and the layout the
 * inbox falls back to below `lg` where there is no room for three panes.
 *
 * The reader itself (fetch, mark-read, message bubbles, composer) lives in
 * `ThreadReader`, shared with the three-pane inbox's right pane. This file is
 * only the page chrome around it: the topbar, the back link, and the 404.
 */
export function ThreadDetailPage() {
  const { threadId } = routeApi.useParams()
  // The reader issues this same query; RTK Query dedupes them into one request,
  // so reading it here for the topbar's title costs nothing extra.
  const { data, isLoading, error } = useGetInboxThreadQuery({ id: threadId })

  // A deleted or cross-tenant thread is a 404, not a generic failure — it
  // gets the app's one shared not-found screen, not a blank body or a retry
  // button that can never succeed.
  if (httpStatus(error) === 404) return <NotFound />

  return (
    <Page>
      <PageTopbar
        eyebrow="Thread"
        back={
          <Button variant="ghost" size="icon-sm" asChild className="shrink-0">
            <Link to="/app/inbox" aria-label="Back to inbox">
              <ArrowLeft className="size-4" />
            </Link>
          </Button>
        }
        title={isLoading ? <Skeleton className="h-5 w-48" /> : data && contactLabel(data)}
        subtitle={data ? data.subject || '(no subject)' : undefined}
        actions={
          data ? (
            <div className="flex items-start gap-2">
              <ReplyClassPill replyClass={data.last_reply_class} replyLabel={data.reply_label} />
              <LabelPicker threadId={threadId} applied={data.labels} />
              <SnoozeMenu threadId={threadId} snooze={data.snooze} />
            </div>
          ) : undefined
        }
      />

      {/* The context rail is shown here too, but only from `xl` — its own
          breakpoint. This route is the layout narrow viewports get, and below
          `xl` the rail stacks below the messages rather than beside them (see
          ContactContextPanel's own responsive classes), which is the right
          fallback: the context is still reachable, just not alongside.

          max-w-3xl is dropped when the rail is present, since a centred column
          plus a right rail reads as lopsided. */}
      <PageBody className="mx-auto w-full max-w-3xl px-4 py-6 sm:px-6 xl:max-w-6xl">
        <ThreadReader threadId={threadId} withContextPanel />
      </PageBody>
    </Page>
  )
}
