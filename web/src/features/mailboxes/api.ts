// Mailbox feature endpoints. The generated store/api.ts declares the raw
// query/mutation shapes; here we layer cache tags on top via
// `enhanceEndpoints` so listing invalidations happen automatically after any
// mutation — no more hand-rolled `refetch()` calls in components.
import { api } from '@/store/api'

const mailboxApi = api.enhanceEndpoints({
  addTagTypes: ['Mailbox'],
  endpoints: {
    listMailboxes: {
      providesTags: (result) =>
        result
          ? [
              ...result.map((m) => ({ type: 'Mailbox' as const, id: m.id ?? 'unknown' })),
              { type: 'Mailbox' as const, id: 'LIST' },
            ]
          : [{ type: 'Mailbox' as const, id: 'LIST' }],
    },
    getMailbox: {
      providesTags: (_result, _error, arg) => [{ type: 'Mailbox', id: arg.id }],
    },
    connectMailbox: {
      invalidatesTags: [{ type: 'Mailbox', id: 'LIST' }],
    },
    pauseMailbox: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Mailbox', id: arg.id },
        { type: 'Mailbox', id: 'LIST' },
      ],
    },
    resumeMailbox: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Mailbox', id: arg.id },
        { type: 'Mailbox', id: 'LIST' },
      ],
    },
    deleteMailbox: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Mailbox', id: arg.id },
        { type: 'Mailbox', id: 'LIST' },
      ],
    },
  },
})

export const {
  useListMailboxesQuery,
  useGetMailboxQuery,
  useConnectMailboxMutation,
  usePauseMailboxMutation,
  useResumeMailboxMutation,
  useDeleteMailboxMutation,
} = mailboxApi
