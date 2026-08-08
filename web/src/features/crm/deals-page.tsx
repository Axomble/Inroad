import { useId, useMemo, useState } from 'react'
import { CircleDollarSign, GitBranch, Plus, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Page, PageBody, PageTopbar, Stat, StatStrip } from '@/components/layout/page'
import { cn } from '@/lib/utils'
import { useCrmGetBoardQuery } from './api'
import { CapturePolicySelect } from './capture-policy-select'
import { DealForm } from './deal-form'
import { DealsBoard } from './deals-board'
import { PipelineForm } from './pipeline-form'
import { PipelinesPanel } from './pipelines-panel'
import { formatTotal } from '@/lib/money'
import { defaultBoardArg } from './query-args'
import { ViewTabs, type ViewTab } from './view-tabs'

type View = 'board' | 'pipelines'

const tabs: ReadonlyArray<ViewTab<View>> = [
  { id: 'board', label: 'Board', icon: CircleDollarSign },
  { id: 'pipelines', label: 'Pipelines', icon: GitBranch },
]

/**
 * The single Deals surface: the board every deal lives on, and the pipelines
 * that define its stages.
 *
 * Deals used to appear twice in the nav — this board, plus a flat deals tab on
 * the page the sidebar called "CRM". The flat list is gone rather than moved:
 * the board already shows every deal, grouped by the stage that explains it.
 */
export function DealsPage() {
  const [view, setView] = useState<View>('board')
  const [creating, setCreating] = useState(false)
  const tabsId = useId()
  // The same arg — and so the same cache entry and single request — as the board
  // itself, so the strip can never disagree with the columns beneath it.
  const boardQuery = useCrmGetBoardQuery(defaultBoardArg)
  const stages = useMemo(() => boardQuery.data?.stages ?? [], [boardQuery.data?.stages])
  const totals = useMemo(
    () => stages.reduce(
      (result, stage) => ({ count: result.count + stage.deal_count, amount: result.amount + stage.amount_micros }),
      { count: 0, amount: 0 },
    ),
    [stages],
  )
  const allDeals = useMemo(() => stages.flatMap(({ deals }) => deals), [stages])

  return (
    <Page>
      <PageTopbar
        eyebrow="CRM"
        title="Deals"
        subtitle="Move opportunities through the pipeline and keep the next action visible."
        actions={
          <div className="flex flex-wrap items-center justify-end gap-2">
            <CapturePolicySelect />
            <Button variant="primary" size="sm" onClick={() => setCreating((open) => !open)} aria-expanded={creating}>
              <Plus aria-hidden="true" />
              New {view === 'pipelines' ? 'pipeline' : 'deal'}
            </Button>
            <Button size="sm" onClick={() => void boardQuery.refetch()} disabled={boardQuery.isFetching}>
              <RefreshCw className={cn(boardQuery.isFetching && 'animate-spin')} aria-hidden="true" />
              Refresh
            </Button>
          </div>
        }
      />
      <StatStrip>
        <Stat
          label="Pipeline value"
          value={boardQuery.isError ? '—' : formatTotal(totals.amount, allDeals)}
          sub="Across all stages"
        />
        <Stat label="Deals" value={boardQuery.isError ? '—' : totals.count} sub={boardQuery.data?.pipeline.name ?? 'Default pipeline'} />
        <Stat
          label="Open stages"
          value={boardQuery.isError ? '—' : stages.filter(({ stage }) => !stage.is_won && !stage.is_lost).length}
          sub="Active workflow"
        />
      </StatStrip>

      <ViewTabs
        baseId={tabsId}
        label="Deal views"
        tabs={tabs}
        view={view}
        onSelect={(next) => { setView(next); setCreating(false) }}
      />

      {creating ? (
        view === 'pipelines'
          ? <PipelineForm onDone={() => setCreating(false)} />
          : <DealForm onDone={() => setCreating(false)} />
      ) : null}

      <PageBody>
        <div
          role="tabpanel"
          id={`${tabsId}-panel-${view}`}
          aria-labelledby={`${tabsId}-tab-${view}`}
          tabIndex={0}
          className="outline-none"
        >
          {view === 'pipelines'
            ? <PipelinesPanel />
            : <DealsBoard onCreate={() => setCreating(true)} />}
        </div>
      </PageBody>
    </Page>
  )
}
