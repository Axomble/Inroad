// Reply-label endpoints. All five operations are already generated from
// api/openapi.yaml into store/api.ts — this file only layers cache tags on top
// via `enhanceEndpoints` (so a create/update/delete/reorder refetches the list
// without a manual `refetch()` at every call site) and re-exports the hooks
// under this feature's own import surface, matching every other feature's
// `api.ts` (see features/inbox/api.ts, features/mailboxes/api.ts).
import { api } from '@/store/api'

export type { ReplyLabel, ReplyLabelInput, ReplyLabelList, ReplyLabelReorderInput } from '@/store/api'

const replyLabelsApi = api.enhanceEndpoints({
  addTagTypes: ['ReplyLabel'],
  endpoints: {
    listReplyLabels: {
      providesTags: (result) =>
        result
          ? [
              ...result.labels.map((l) => ({ type: 'ReplyLabel' as const, id: l.id })),
              { type: 'ReplyLabel' as const, id: 'LIST' },
            ]
          : [{ type: 'ReplyLabel' as const, id: 'LIST' }],
    },
    createReplyLabel: {
      invalidatesTags: [{ type: 'ReplyLabel', id: 'LIST' }],
    },
    updateReplyLabel: {
      invalidatesTags: [{ type: 'ReplyLabel', id: 'LIST' }],
    },
    deleteReplyLabel: {
      invalidatesTags: [{ type: 'ReplyLabel', id: 'LIST' }],
    },
    // Reorder responds with the whole list in its new order, but consumers read
    // through the list query — invalidating LIST keeps that one source of truth.
    reorderReplyLabels: {
      invalidatesTags: [{ type: 'ReplyLabel', id: 'LIST' }],
    },
  },
})

export const {
  useListReplyLabelsQuery,
  useCreateReplyLabelMutation,
  useUpdateReplyLabelMutation,
  useDeleteReplyLabelMutation,
  useReorderReplyLabelsMutation,
} = replyLabelsApi
