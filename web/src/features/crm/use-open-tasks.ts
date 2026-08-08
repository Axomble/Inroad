import { useCrmListTasksQuery, type CrmTargetType, type CrmTask } from './api'
import { listPageSize } from './query-args'

/**
 * The tasks still to be done on a record. Shared by the tasks panel and the stat
 * strips that count them — one hook means one query arg, so one cache entry and
 * one request, and a header that can never disagree with the list below it.
 */
export function useOpenTasks(targetType: CrmTargetType, targetId: string) {
  const query = useCrmListTasksQuery({ targetType, targetId, limit: listPageSize })
  const open: CrmTask[] = (query.data?.items ?? []).filter(
    ({ status }) => status === 'open' || status === 'in_progress',
  )
  return { query, open }
}
