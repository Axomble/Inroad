// Warmup feature endpoints. The generated store/api.ts declares the raw
// query/mutation shapes from the OpenAPI contract; here we layer cache tags on
// top via `enhanceEndpoints` so enabling/disabling/updating a mailbox's warmup
// automatically refetches the pool overview and that mailbox's detail — no
// hand-rolled `refetch()` calls in components. The generated file is never
// hand-edited; the tag wiring lives here (mirrors features/mailboxes/api.ts).
import { api } from '@/store/api'

const warmupApi = api.enhanceEndpoints({
  addTagTypes: ['Warmup'],
  endpoints: {
    getWarmupOverview: {
      providesTags: [{ type: 'Warmup', id: 'OVERVIEW' }],
    },
    getMailboxWarmup: {
      providesTags: (_result, _error, arg) => [{ type: 'Warmup', id: arg.id }],
    },
    // Deliberately the same tag as the mailbox's detail: the history is written
    // by the engine, not by this UI, and the one user action that changes what
    // it means (leaving or rejoining the pool) already invalidates that tag.
    listWarmupTransitions: {
      providesTags: (_result, _error, arg) => [{ type: 'Warmup', id: arg.mailboxId }],
    },
    enableMailboxWarmup: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Warmup', id: 'OVERVIEW' },
        { type: 'Warmup', id: arg.id },
      ],
    },
    disableMailboxWarmup: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Warmup', id: 'OVERVIEW' },
        { type: 'Warmup', id: arg.id },
      ],
    },
    // Designation changes the POOL, not just this mailbox: the sentinel count, the
    // advisory share and — once the sweep runs — every other mailbox's evidence
    // label are all read off the overview, so the overview tag has to fall with the
    // mailbox's own.
    setWarmupSentinel: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Warmup', id: 'OVERVIEW' },
        { type: 'Warmup', id: arg.mailboxId },
      ],
    },
  },
})

export const {
  useGetWarmupOverviewQuery,
  useGetMailboxWarmupQuery,
  useListWarmupTransitionsQuery,
  useEnableMailboxWarmupMutation,
  useDisableMailboxWarmupMutation,
  useSetWarmupSentinelMutation,
} = warmupApi
