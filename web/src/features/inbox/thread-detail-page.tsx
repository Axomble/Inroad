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
        actions={data ? <ReplyClassPill replyClass={data.last_reply_class} replyLabel={data.reply_label} /> : undefined}
      />

      <PageBody className="mx-auto w-full max-w-3xl px-4 py-6 sm:px-6">
        <ThreadReader threadId={threadId} />
      </PageBody>
    </Page>
  )
}
