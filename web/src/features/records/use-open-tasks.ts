import { useCrmListTasksQuery, type CrmTargetType, type CrmTask } from './api'
import { listPageSize } from './query-args'

/**
 * Still to be done: neither finished nor abandoned. One home for the rule,
 * because the tasks panel keeps rendering a task it just completed (see
 * `TasksPanel`) and so needs the same definition of "open" the counts use — two
 * copies of it would eventually disagree about `in_progress`.
 */
export function isOpenTask({ status }: Pick<CrmTask, 'status'>): boolean {
  return status === 'open' || status === 'in_progress'
}

/**
 * The tasks still to be done on a record. Shared by the tasks panel and the stat
 * strips that count them — one hook means one query arg, so one cache entry and
 * one request, and a header that can never disagree with the list below it.
 */
export function useOpenTasks(targetType: CrmTargetType, targetId: string) {
  const query = useCrmListTasksQuery({ targetType, targetId, limit: listPageSize })
  const open: CrmTask[] = (query.data?.items ?? []).filter(isOpenTask)
  return { query, open }
}
