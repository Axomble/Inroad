import { memo, useState } from 'react'
import { z } from 'zod'
import { Loader2, RotateCcw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useCrmListEventsQuery, useCrmMoveDealMutation, type CrmEvent, type CrmTargetType } from './api'
import { crmErrorMessage } from './error-copy'
import { formatDateTime } from '@/lib/datetime'
import { parseActor } from './actor'
import { ActorBadge } from './actor-badge'
import { InlineLoading, MutedEmpty, QueryErrorBanner, Section } from './record-parts'

const stageChangeDataSchema = z.object({ from_stage_id: z.string().uuid() }).passthrough()

/**
 * The attributed, chronological record of what happened to a contact, company or
 * deal. Repeated events within ten minutes are grouped by the server.
 *
 * `revertDealId` is the deal a `deal.stage_changed` event can be undone on — only
 * a deal's own feed can offer that, but the row is otherwise identical across the
 * three record types, so it stays one component rather than two that drift.
 */
export function ActivityPanel({
  targetType,
  targetId,
  revertDealId,
}: {
  targetType: CrmTargetType
  targetId: string
  revertDealId?: string
}) {
  const eventsQuery = useCrmListEventsQuery({ targetType, targetId })
  const events = eventsQuery.data?.items ?? []

  return (
    <Section title="What happened" description="A chronological, attributed record. Repeated events within ten minutes are grouped.">
      {eventsQuery.isLoading ? <InlineLoading label="Loading activity" /> : null}
      {eventsQuery.isError ? (
        <QueryErrorBanner
          className=""
          error={eventsQuery.error}
          fallback="The activity feed could not be loaded."
          onRetry={() => void eventsQuery.refetch()}
          retrying={eventsQuery.isFetching}
        />
      ) : null}
      {events.length > 0 ? (
        <ol className="divide-y divide-border">
          {events.map((event) => <ActivityRow key={event.id} revertDealId={revertDealId} event={event} />)}
        </ol>
      ) : null}
      {!eventsQuery.isLoading && !eventsQuery.isError && events.length === 0 ? (
        <MutedEmpty text="No activity has been recorded yet." />
      ) : null}
    </Section>
  )
}

const ActivityRow = memo(function ActivityRow({ revertDealId, event }: { revertDealId?: string; event: CrmEvent }) {
  const [moveDeal, moveState] = useCrmMoveDealMutation()
  const [revertError, setRevertError] = useState<string | null>(null)
  const label = event.name.split('.').map((part) => part.replaceAll('_', ' ')).join(' ')
  // Actor/data are open JSON objects in the API contract. Parse that boundary
  // once before using fields in labels or mutations.
  const actor = parseActor(event.actor)
  const previousStage = event.name === 'deal.stage_changed' ? stageChangeDataSchema.safeParse(event.data) : null
  const canRevert = revertDealId !== undefined && previousStage?.success === true

  const revert = async () => {
    if (!canRevert || revertDealId === undefined || previousStage?.success !== true) return
    setRevertError(null)
    try {
      await moveDeal({ id: revertDealId, crmMoveDealInput: { stage_id: previousStage.data.from_stage_id } }).unwrap()
    } catch (error) {
      setRevertError(crmErrorMessage(error, 'The stage change could not be reverted.'))
    }
  }

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
          {canRevert ? (
            <Button type="button" variant="outline" size="sm" onClick={() => void revert()} disabled={moveState.isLoading} aria-label="Revert this stage change">
              {moveState.isLoading ? <Loader2 className="animate-spin" aria-hidden="true" /> : <RotateCcw aria-hidden="true" />}
              Revert
            </Button>
          ) : null}
        </div>
        {event.source_thread_ref || event.source_message_id ? (
          <p className="mt-2 break-all text-xs text-muted-foreground">
            Source: {event.source_thread_ref ? `thread ${event.source_thread_ref}` : ''}
            {event.source_thread_ref && event.source_message_id ? ' / ' : ''}
            {event.source_message_id ? `message ${event.source_message_id}` : ''}
          </p>
        ) : null}
        {actor.type === 'agent' && actor.thread_id ? <p className="mt-1 break-all text-xs text-faint">Agent thread {actor.thread_id}{actor.run_id ? ` / run ${actor.run_id}` : ''}</p> : null}
        {revertError ? <p role="alert" className="mt-2 text-xs text-danger">{revertError}</p> : null}
      </div>
    </li>
  )
})
