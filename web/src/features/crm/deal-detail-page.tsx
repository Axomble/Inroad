import { Link } from '@tanstack/react-router'
import { ArrowLeft, MessageSquareText } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Page, PageBody, PageTopbar, Stat, StatStrip } from '@/components/layout/page'
import { httpStatus } from '@/lib/rtk-error'
import { useCrmGetDealQuery, useCrmListDealThreadsQuery } from './api'
import { ActivityPanel } from '@/features/records/activity-panel'
import { NotesPanel } from '@/features/records/notes-panel'
import { RevertStageChange } from './revert-stage-change'
import { TasksPanel } from '@/features/records/tasks-panel'
import { useOpenTasks } from '@/features/records/use-open-tasks'
import { parseActor } from '@/features/records/actor'
import { ActorBadge } from '@/features/records/actor-badge'
import { crmErrorMessage } from './error-copy'
import { formatDateTime } from '@/lib/datetime'
import { formatMoney } from '@/lib/money'
import { Detail, InlineLoading, MutedEmpty, RecordPageMessage, RecordPageSkeleton, Section } from '@/components/shared/record-page'

/**
 * A deal as a hub: its own fields, the account and person it belongs to, the
 * conversation that produced it, and the notes, tasks and activity that move it.
 */
export function DealDetailPage({ dealId }: { dealId: string }) {
  const dealQuery = useCrmGetDealQuery({ id: dealId })
  const threadsQuery = useCrmListDealThreadsQuery({ id: dealId })
  const { open: openTasks } = useOpenTasks('deal', dealId)
  const deal = dealQuery.data

  if (dealQuery.isLoading) return <RecordPageSkeleton label="Loading deal" />
  // A failed request is not the same as a missing deal: a 500 or an offline
  // browser must offer a retry, not tell the user their deal was deleted.
  if (dealQuery.isError && httpStatus(dealQuery.error) !== 404) {
    return (
      <RecordPageMessage
        title="This deal could not be loaded"
        description={crmErrorMessage(dealQuery.error, 'Try again in a moment.')}
        action={<Button onClick={() => void dealQuery.refetch()} disabled={dealQuery.isFetching}>Try again</Button>}
      />
    )
  }
  if (!deal) {
    return (
      <RecordPageMessage
        title="Deal not found"
        description="It may have been removed or belong to another workspace."
        action={<Button asChild><Link to="/app/deals">Back to deals</Link></Button>}
      />
    )
  }

  return (
    <Page>
      <PageTopbar
        eyebrow="Deal"
        title={deal.name}
        subtitle={deal.company_name || deal.contact_email || 'Unlinked opportunity'}
        actions={
          <>
            {/* Who created this deal, next to the deal itself — the activity
                feed below only attributes individual events. */}
            <ActorBadge actor={parseActor(deal.created_by_actor)} source={deal.source} />
            <Button asChild size="sm"><Link to="/app/deals"><ArrowLeft aria-hidden="true" />Board</Link></Button>
          </>
        }
      />
      <StatStrip>
        <Stat label="Value" value={formatMoney(deal.amount_micros ?? 0, deal.currency)} sub={deal.currency} />
        <Stat label="Stage" value={deal.stage_label} sub={deal.pipeline_name} />
        <Stat label="Next actions" value={openTasks.length} sub="Open or in progress" />
      </StatStrip>
      <PageBody>
        <div className="grid min-w-0 gap-5 p-4 sm:p-5 xl:grid-cols-[minmax(0,1.45fr)_minmax(18rem,0.75fr)]">
          <div className="min-w-0 space-y-5">
            {/* The revert is deal-only, so it is handed to the feed as a slot
                rather than living inside a panel contacts also uses. */}
            <ActivityPanel
              targetType="deal"
              targetId={dealId}
              renderEventAction={(event) => <RevertStageChange dealId={dealId} event={event} />}
            />
            <Section title="Conversation context" description="Structured headers and participants only; inbound message bodies are not exposed.">
              {threadsQuery.isLoading ? <InlineLoading /> : null}
              <div className="space-y-3">
                {threadsQuery.data?.items.map((thread) => (
                  <article key={thread.id} className="rounded-lg border border-border bg-background p-4">
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <div>
                        <h3 className="text-sm font-semibold">{thread.subject || 'Email thread'}</h3>
                        <p className="mt-1 text-xs text-muted-foreground">{thread.participants.map(({ email }) => email).join(', ')}</p>
                      </div>
                      {thread.reply_class ? <span className="rounded-full bg-ok/10 px-2 py-1 text-xs font-medium text-ok">{thread.reply_class}</span> : null}
                    </div>
                    <ol className="mt-3 space-y-2">
                      {thread.messages.map((message) => (
                        <li key={message.id} className="flex items-center gap-2 text-xs text-muted-foreground">
                          <MessageSquareText className="size-3.5" aria-hidden="true" />
                          <span className="font-medium text-foreground">{message.direction === 'inbound' ? 'Reply received' : 'Message sent'}</span>
                          <time dateTime={message.occurred_at}>{formatDateTime(message.occurred_at)}</time>
                        </li>
                      ))}
                    </ol>
                  </article>
                ))}
              </div>
              {!threadsQuery.isLoading && !threadsQuery.data?.items.length ? <MutedEmpty text="No campaign thread is linked to this deal." /> : null}
            </Section>
            <NotesPanel targetType="deal" targetId={dealId} />
          </div>
          <aside className="min-w-0 space-y-5">
            <Section title="What matters now" description="The core context for this opportunity.">
              <dl className="grid gap-3 text-sm">
                <Detail
                  label="Company"
                  value={
                    deal.company_id ? (
                      <Link
                        to="/app/companies/$id"
                        params={{ id: deal.company_id }}
                        className="text-accent-ink underline-offset-2 hover:underline"
                      >
                        {deal.company_name || 'Open company'}
                      </Link>
                    ) : (
                      'Not linked'
                    )
                  }
                />
                <Detail
                  label="Primary contact"
                  value={
                    deal.primary_contact_id ? (
                      <Link
                        to="/app/contacts/$id"
                        params={{ id: deal.primary_contact_id }}
                        className="text-accent-ink underline-offset-2 hover:underline"
                      >
                        {deal.contact_email || 'Open contact'}
                      </Link>
                    ) : (
                      'Not linked'
                    )
                  }
                />
                <Detail label="Source" value={deal.source === 'reply' ? 'Positive campaign reply' : deal.source} />
                <Detail label="Close date" value={deal.close_date ? formatDateTime(deal.close_date) : 'Not set'} />
              </dl>
            </Section>
            <TasksPanel targetType="deal" targetId={dealId} />
          </aside>
        </div>
      </PageBody>
    </Page>
  )
}
