import { useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { EmptyBlock, Page, PageBody, PageTopbar, SectionBar } from '@/components/layout/page'
import { ApprovalCard } from './approval-card'
import { useListAgentApprovalsQuery, type AgentApprovalStatus } from './api'

const filters = ['all', 'pending', 'approved', 'executed', 'rejected', 'expired', 'failed'] as const
type ApprovalFilter = (typeof filters)[number]

export function ApprovalsPage() {
  const [filter, setFilter] = useState<ApprovalFilter>('pending')
  const status: AgentApprovalStatus | undefined = filter === 'all' ? undefined : filter
  const query = useListAgentApprovalsQuery({ status, limit: 100 })
  const actions = query.data?.actions ?? []
  const pendingCount = actions.filter((action) => action.status === 'pending').length

  return (
    <Page>
      <PageTopbar eyebrow="Assistant" title="Approvals" subtitle="Review consequential actions before the assistant can continue" actions={
        <Button size="sm" variant="outline" onClick={() => void query.refetch()} disabled={query.isFetching}>
          <RefreshCw className={query.isFetching ? 'animate-spin' : ''} /> Refresh
        </Button>
      } />
      <SectionBar label="Action queue" count={filter === 'pending' ? pendingCount : actions.length}>
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          <span>Status</span>
          <Select value={filter} onChange={(event) => setFilter(event.target.value as ApprovalFilter)} aria-label="Filter approvals by status">
            {filters.map((value) => <option key={value} value={value}>{value === 'all' ? 'All actions' : value.charAt(0).toUpperCase() + value.slice(1)}</option>)}
          </Select>
        </label>
      </SectionBar>
      <PageBody>
        {query.isLoading ? (
          <div className="grid gap-4 p-4 sm:p-6 lg:grid-cols-2" aria-label="Loading approvals">
            <Skeleton className="h-64 rounded-lg" /><Skeleton className="h-64 rounded-lg" />
          </div>
        ) : query.isError ? (
          <EmptyBlock title="Couldn't load approvals" description="The action queue is temporarily unavailable." action={<Button variant="outline" size="sm" onClick={() => void query.refetch()}>Try again</Button>} />
        ) : actions.length === 0 ? (
          <EmptyBlock title={filter === 'pending' ? 'No actions need approval' : `No ${filter} actions`} description={filter === 'pending' ? 'Consequential assistant actions will wait here for your review.' : 'Choose another status to review prior actions.'} />
        ) : (
          <div className="grid items-start gap-4 p-4 sm:p-6 xl:grid-cols-2">
            {actions.map((action) => <ApprovalCard key={action.id} action={action} />)}
          </div>
        )}
      </PageBody>
    </Page>
  )
}
