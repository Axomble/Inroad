import { memo, useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useDraggable,
  useDroppable,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core'
import { CSS } from '@dnd-kit/utilities'
import { GripVertical, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/select'
import { EmptyBlock, Page, PageBody, PageTopbar, Stat, StatStrip } from '@/components/layout/page'
import { cn } from '@/lib/utils'
import { useCrmGetBoardQuery, useCrmMoveDealMutation, type CrmBoardStage, type CrmDeal } from './api'
import { parseActor } from './actor'
import { ActorBadge } from './actor-badge'
import { crmErrorMessage } from './error-copy'
import { formatMoney, formatTotal } from './money'

// The board endpoint takes an optional `pipelineId`; C1 always shows the
// default pipeline, so one constant arg keeps every reader on one cache entry.
const defaultBoardArg = {}

export function DealsBoardPage() {
  const boardQuery = useCrmGetBoardQuery(defaultBoardArg)
  const [moveDeal, moveState] = useCrmMoveDealMutation()
  const [announcement, setAnnouncement] = useState('')
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor),
  )
  const stages = useMemo(() => boardQuery.data?.stages ?? [], [boardQuery.data?.stages])
  const totals = useMemo(() => stages.reduce(
    (result, stage) => ({ count: result.count + stage.deal_count, amount: result.amount + stage.amount_micros }),
    { count: 0, amount: 0 },
  ), [stages])
  const allDeals = useMemo(() => stages.flatMap(({ deals }) => deals), [stages])

  const move = async (deal: CrmDeal, stageId: string, beforeDealId?: string, afterDealId?: string) => {
    if (deal.stage_id === stageId && !beforeDealId && !afterDealId) return
    const target = stages.find(({ stage }) => stage.id === stageId)
    try {
      await moveDeal({
        id: deal.id,
        crmMoveDealInput: { stage_id: stageId, before_deal_id: beforeDealId, after_deal_id: afterDealId },
      }).unwrap()
      setAnnouncement(`${deal.name} moved to ${target?.stage.label ?? 'the selected stage'}.`)
    } catch (error) {
      // The optimistic patch has already rolled back; say why it snapped back.
      setAnnouncement(`Could not move ${deal.name}. ${crmErrorMessage(error, 'The board was restored.')}`)
    }
  }

  const onDragEnd = ({ active, over }: DragEndEvent) => {
    if (!over) return
    const deal = stages.flatMap(({ deals }) => deals).find(({ id }) => id === active.id)
    if (!deal) return
    const target = stages.find(({ stage, deals }) => stage.id === over.id || deals.some(({ id }) => id === over.id))
    if (!target) return
    const overDeal = target.deals.find(({ id }) => id === over.id)
    const overIndex = overDeal ? target.deals.findIndex(({ id }) => id === overDeal.id) : -1
    void move(
      deal,
      target.stage.id,
      overIndex > 0 ? target.deals[overIndex - 1]?.id : undefined,
      overDeal?.id,
    )
  }

  return (
    <Page>
      <PageTopbar
        eyebrow="CRM"
        title="Deals"
        subtitle="Move opportunities through the pipeline and keep the next action visible."
        actions={
          <Button size="sm" onClick={() => void boardQuery.refetch()} disabled={boardQuery.isFetching}>
            <RefreshCw className={cn(boardQuery.isFetching && 'animate-spin')} aria-hidden="true" />
            Refresh
          </Button>
        }
      />
      <StatStrip>
        <Stat label="Pipeline value" value={formatTotal(totals.amount, allDeals)} sub="Across all stages" />
        <Stat label="Deals" value={totals.count} sub={boardQuery.data?.pipeline.name ?? 'Default pipeline'} />
        <Stat label="Open stages" value={stages.filter(({ stage }) => !stage.is_won && !stage.is_lost).length} sub="Active workflow" />
      </StatStrip>
      <PageBody>
        <p className="sr-only" aria-live="polite">{announcement}</p>
        {boardQuery.isLoading ? <BoardSkeleton /> : null}
        {boardQuery.isError ? (
          <EmptyBlock
            title="The pipeline could not be loaded"
            description={crmErrorMessage(boardQuery.error, 'Try refreshing. Your deals have not been changed.')}
            action={<Button onClick={() => void boardQuery.refetch()}>Try again</Button>}
          />
        ) : null}
        {boardQuery.data && totals.count === 0 ? (
          <EmptyBlock
            title="No deals yet"
            description="Create a deal from CRM, or let a positive campaign reply create one automatically."
            action={<Button asChild variant="primary"><Link to="/app/crm">Create a deal</Link></Button>}
          />
        ) : null}
        {boardQuery.data && totals.count > 0 ? (
          <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
            <div className="grid grid-cols-1 items-start gap-4 md:grid-flow-col md:auto-cols-[minmax(17rem,1fr)] md:overflow-x-auto md:pb-3">
              {stages.map((column) => (
                <BoardColumn
                  key={column.stage.id}
                  column={column}
                  stages={stages}
                  disabled={moveState.isLoading}
                  onMove={move}
                />
              ))}
            </div>
          </DndContext>
        ) : null}
      </PageBody>
    </Page>
  )
}

const BoardColumn = memo(function BoardColumn({
  column,
  stages,
  disabled,
  onMove,
}: {
  column: CrmBoardStage
  stages: CrmBoardStage[]
  disabled: boolean
  onMove: (deal: CrmDeal, stageId: string, beforeDealId?: string, afterDealId?: string) => Promise<void>
}) {
  const { setNodeRef, isOver } = useDroppable({ id: column.stage.id })
  return (
    <section
      ref={setNodeRef}
      aria-labelledby={`stage-${column.stage.id}`}
      className={cn(
        'min-w-0 rounded-xl border border-border bg-surface p-3 transition-colors',
        isOver && 'border-primary bg-primary/5',
      )}
    >
      <header className="mb-3 flex items-start justify-between gap-3">
        <div>
          <h2 id={`stage-${column.stage.id}`} className="flex items-center gap-2 text-sm font-semibold">
            <span className="size-2.5 rounded-full" style={{ backgroundColor: column.stage.color }} aria-hidden="true" />
            {column.stage.label}
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">{column.deal_count} deals / {formatTotal(column.amount_micros, column.deals)}</p>
        </div>
      </header>
      <div className="space-y-2.5">
        {column.deals.map((deal) => (
          <DealCard key={deal.id} deal={deal} stages={stages} disabled={disabled} onMove={onMove} />
        ))}
        {column.deals.length === 0 ? (
          <p className="rounded-lg border border-dashed border-border px-3 py-8 text-center text-xs text-muted-foreground">Drop a deal here</p>
        ) : null}
      </div>
    </section>
  )
})

const DealCard = memo(function DealCard({
  deal,
  stages,
  disabled,
  onMove,
}: {
  deal: CrmDeal
  stages: CrmBoardStage[]
  disabled: boolean
  onMove: (deal: CrmDeal, stageId: string) => Promise<void>
}) {
  const { attributes, listeners, setNodeRef, transform, isDragging } = useDraggable({ id: deal.id })
  return (
    <article
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform) }}
      className={cn(
        'rounded-lg border border-border bg-background p-3 shadow-sm',
        isDragging && 'relative z-10 opacity-75 shadow-lg',
      )}
    >
      <div className="flex items-start gap-2">
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className="mt-0.5 cursor-grab touch-none"
          aria-label={`Drag ${deal.name}`}
          {...attributes}
          {...listeners}
        >
          <GripVertical aria-hidden="true" />
        </Button>
        <div className="min-w-0 flex-1">
          <Link
            to="/app/deals/$id"
            params={{ id: deal.id }}
            className="line-clamp-2 text-sm font-semibold text-foreground hover:text-accent-ink hover:underline"
          >
            {deal.name}
          </Link>
          <p className="mt-1 truncate text-xs text-muted-foreground">{deal.company_name || deal.contact_email || 'No company linked'}</p>
          <div className="mt-2 flex flex-wrap items-center justify-between gap-2">
            <p className="text-sm font-semibold">{formatMoney(deal.amount_micros ?? 0, deal.currency)}</p>
            {/* An agent- or reply-created deal has to be tellable from a
                hand-made one without opening it. */}
            <ActorBadge actor={parseActor(deal.created_by_actor)} source={deal.source} />
          </div>
          <label className="mt-3 block text-[11px] font-medium text-muted-foreground">
            Move to stage
            <Select
              className="mt-1 h-8 text-xs"
              value={deal.stage_id}
              disabled={disabled}
              onChange={(event) => void onMove(deal, event.target.value)}
            >
              {stages.map(({ stage }) => <option key={stage.id} value={stage.id}>{stage.label}</option>)}
            </Select>
          </label>
        </div>
      </div>
    </article>
  )
})

function BoardSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-3" aria-label="Loading pipeline">
      {[0, 1, 2].map((value) => <div key={value} className="h-64 animate-pulse rounded-xl bg-surface-2" />)}
    </div>
  )
}

