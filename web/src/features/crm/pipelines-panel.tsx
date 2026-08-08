import { memo } from 'react'
import { Skeleton } from '@/components/ui/skeleton'
import { EmptyBlock } from '@/components/layout/page'
import { useCrmListPipelinesQuery, type CrmPipeline } from './api'
import { QueryErrorBanner } from './record-parts'

/**
 * The stage definitions deals move through. Configuration for deals, so it
 * lives on the deals screen rather than under Companies.
 */
export function PipelinesPanel() {
  const pipelinesQuery = useCrmListPipelinesQuery()
  if (pipelinesQuery.isError) {
    return (
      <QueryErrorBanner
        error={pipelinesQuery.error}
        fallback="Pipelines could not be loaded."
        onRetry={() => void pipelinesQuery.refetch()}
        retrying={pipelinesQuery.isFetching}
      />
    )
  }
  if (pipelinesQuery.isLoading) {
    return (
      <div className="grid gap-4 p-4 md:grid-cols-2 xl:grid-cols-3" aria-label="Loading pipelines">
        <Skeleton className="h-48 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    )
  }
  return <PipelinesList pipelines={pipelinesQuery.data?.items ?? []} />
}

const PipelinesList = memo(function PipelinesList({ pipelines }: { pipelines: readonly CrmPipeline[] }) {
  if (pipelines.length === 0) {
    return (
      <EmptyBlock
        title="No pipelines"
        description="Create a pipeline to define how deals move from lead to won or lost."
      />
    )
  }
  return (
    <div className="grid gap-4 p-4 md:grid-cols-2 xl:grid-cols-3">
      {pipelines.map((pipeline) => (
        <article key={pipeline.id} className="rounded-lg border border-border bg-surface p-4">
          <div className="flex items-center justify-between gap-3">
            <h2 className="truncate text-sm font-semibold">{pipeline.name}</h2>
            {pipeline.is_default && (
              <span className="rounded bg-primary/10 px-2 py-0.5 font-mono text-[10px] uppercase text-primary">
                Default
              </span>
            )}
          </div>
          <ol className="mt-4 space-y-2">
            {pipeline.stages.map((stage) => (
              <li key={stage.id} className="flex min-h-8 items-center gap-2 rounded-md bg-surface-2 px-3 text-xs">
                <span className="size-2 rounded-full" style={{ backgroundColor: stage.color }} aria-hidden="true" />
                <span className="truncate">{stage.label}</span>
                {(stage.is_won || stage.is_lost) && (
                  <span className="ml-auto text-[10px] uppercase text-muted-foreground">
                    {stage.is_won ? 'Won' : 'Lost'}
                  </span>
                )}
              </li>
            ))}
          </ol>
        </article>
      ))}
    </div>
  )
})
