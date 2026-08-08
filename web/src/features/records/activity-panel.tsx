import { memo } from 'react'
import { formatDateTime } from '@/lib/datetime'
import { InlineLoading, MutedEmpty, QueryErrorBanner, Section } from '@/components/shared/record-page'
import { useCrmListEventsQuery, type CrmEvent, type CrmTargetType } from './api'
import { recordErrorMessage } from './error-copy'
import { parseActor } from './actor'
import { ActorBadge } from './actor-badge'

/**
 * The attributed, chronological record of what happened to a contact, company or
 * deal. Repeated events within ten minutes are grouped by the server.
 *
 * `renderEventAction` is how a domain adds an action to a row without this module
 * learning about that domain: the deal screens pass a revert control for
 * `deal.stage_changed`, which needs the deal move mutation. Reaching for that
 * mutation from here would put deal knowledge in the one panel that has to stay
 * record-generic.
 */
export function ActivityPanel({
  targetType,
  targetId,
  renderEventAction,
}: {
  targetType: CrmTargetType
  targetId: string
  renderEventAction?: (event: CrmEvent) => React.ReactNode
}) {
  const eventsQuery = useCrmListEventsQuery({ targetType, targetId })
  const events = eventsQuery.data?.items ?? []

  return (
    <Section title="What happened" description="A chronological, attributed record. Repeated events within ten minutes are grouped.">
      {eventsQuery.isLoading ? <InlineLoading label="Loading activity" /> : null}
      {eventsQuery.isError ? (
        <QueryErrorBanner
          className=""
          message={recordErrorMessage(eventsQuery.error, 'The activity feed could not be loaded.')}
          onRetry={() => void eventsQuery.refetch()}
          retrying={eventsQuery.isFetching}
        />
      ) : null}
      {events.length > 0 ? (
        <ol className="divide-y divide-border">
          {events.map((event) => (
            <ActivityRow key={event.id} event={event} action={renderEventAction?.(event)} />
          ))}
        </ol>
      ) : null}
      {!eventsQuery.isLoading && !eventsQuery.isError && events.length === 0 ? (
        <MutedEmpty text="No activity has been recorded yet." />
      ) : null}
    </Section>
  )
}

const ActivityRow = memo(function ActivityRow({ event, action }: { event: CrmEvent; action?: React.ReactNode }) {
  const label = event.name.split('.').map((part) => part.replaceAll('_', ' ')).join(' ')
  // Actor is an open JSON object in the API contract. Parse that boundary once
  // before using fields in labels.
  const actor = parseActor(event.actor)

  return (
    <li className="flex min-w-0 gap-3 py-3 first:pt-0 last:pb-0">
      <span className="mt-1.5 size-2 shrink-0 rounded-full bg-primary" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0">
            <p className="text-sm font-medium capitalize">{label}</p>
            <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
              <ActorBadge actor={actor} />
              {event.merged_count && event.merged_count > 1 ? <span>{event.merged_count} grouped events</span> : null}
              <time dateTime={event.occurred_at}>{formatDateTime(event.occurred_at)}</time>
            </p>
          </div>
          {action}
        </div>
        {event.source_thread_ref || event.source_message_id ? (
          <p className="mt-2 break-all text-xs text-muted-foreground">
            Source: {event.source_thread_ref ? `thread ${event.source_thread_ref}` : ''}
            {event.source_thread_ref && event.source_message_id ? ' / ' : ''}
            {event.source_message_id ? `message ${event.source_message_id}` : ''}
          </p>
        ) : null}
        {actor.type === 'agent' && actor.thread_id ? <p className="mt-1 break-all text-xs text-faint">Agent thread {actor.thread_id}{actor.run_id ? ` / run ${actor.run_id}` : ''}</p> : null}
      </div>
    </li>
  )
})
