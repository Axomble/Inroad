// Failed background tasks. The generated store/api.ts declares the raw
// query/mutation shapes from the OpenAPI contract; the cache tags are layered on
// here via `enhanceEndpoints`, so replaying or discarding a row refetches the list
// without a hand-rolled `refetch()` in the component (mirrors features/warmup/api.ts).
import { api } from '@/store/api'

const deadLetterApi = api.enhanceEndpoints({
  addTagTypes: ['DeadLetter'],
  endpoints: {
    listTaskDeadLetters: {
      // One LIST tag rather than per-status: the status filter is a query argument,
      // and an action moves a row BETWEEN statuses, so a replay has to invalidate
      // the list the row left as well as the one it joined. Tagging per-status would
      // leave the pending list still showing a row that is now replayed.
      providesTags: [{ type: 'DeadLetter', id: 'LIST' }],
    },
    getTaskDeadLetter: {
      providesTags: (_result, _error, arg) => [{ type: 'DeadLetter', id: arg.id }],
    },
    replayTaskDeadLetter: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'DeadLetter', id: 'LIST' },
        { type: 'DeadLetter', id: arg.id },
      ],
    },
    discardTaskDeadLetter: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'DeadLetter', id: 'LIST' },
        { type: 'DeadLetter', id: arg.id },
      ],
    },
  },
})

export const {
  useListTaskDeadLettersQuery,
  useReplayTaskDeadLetterMutation,
  useDiscardTaskDeadLetterMutation,
} = deadLetterApi
