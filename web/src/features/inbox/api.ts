// Inbox feature endpoints. The three operations are already fully generated
// from api/openapi.yaml into store/api.ts (listInboxThreads, getInboxThread,
// setInboxThreadRead) — this file only layers cache tags on top via
// `enhanceEndpoints` (so marking a thread read/unread refetches the list
// without a manual `refetch()` at every call site) and re-exports the hooks
// under this feature's own import surface, matching every other feature's
// `api.ts` (see features/mailboxes/api.ts, features/campaigns/api.ts).
import { api } from '@/store/api'

export type { InboxThreadSummary, InboxThreadDetail, InboxMessage, SetInboxThreadReadRequest } from '@/store/api'

const inboxApi = api.enhanceEndpoints({
  addTagTypes: ['InboxThread'],
  endpoints: {
    listInboxThreads: {
      providesTags: (result) =>
        result
          ? [
              ...result.items.map((t) => ({ type: 'InboxThread' as const, id: t.id })),
              { type: 'InboxThread' as const, id: 'LIST' },
            ]
          : [{ type: 'InboxThread' as const, id: 'LIST' }],
    },
    getInboxThread: {
      providesTags: (_result, _error, arg) => [{ type: 'InboxThread', id: arg.id }],
    },
    // Invalidates the LIST tag too: an unread thread marked read must
    // disappear from an "unread" filter (none exists yet, but the same tag
    // also drives the rail's per-mailbox counts, which must stay in step).
    setInboxThreadRead: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'InboxThread', id: arg.id },
        { type: 'InboxThread', id: 'LIST' },
      ],
    },
  },
})

export const { useListInboxThreadsQuery, useGetInboxThreadQuery, useSetInboxThreadReadMutation } = inboxApi
