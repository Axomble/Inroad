// Mailbox feature endpoints. The generated store/api.ts declares the raw
// query/mutation shapes; here we layer cache tags on top via
// `enhanceEndpoints` so listing invalidations happen automatically after any
// mutation — no more hand-rolled `refetch()` calls in components.
//
// The OAuth "start" endpoints aren't in the OpenAPI-generated client (they
// return an opaque auth_url and are browser-redirect flows), so they're layered
// on here with `injectEndpoints` rather than hand-editing the generated
// store/api.ts.
import { api } from '@/store/api'

/** Response from the OAuth "start" endpoints — an opaque provider consent URL. */
export type StartOauthResponse = { auth_url: string }
/** @deprecated Alias kept for existing imports; use StartOauthResponse. */
export type StartGoogleOauthResponse = StartOauthResponse

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
}).injectEndpoints({
  endpoints: (build) => ({
    startGoogleOauth: build.mutation<StartOauthResponse, void>({
      query: () => ({ url: '/mailboxes/oauth/google/start', method: 'POST' }),
    }),
    startMicrosoftOauth: build.mutation<StartOauthResponse, void>({
      query: () => ({ url: '/mailboxes/oauth/microsoft/start', method: 'POST' }),
    }),
  }),
})

export const {
  useListMailboxesQuery,
  useGetMailboxQuery,
  useConnectMailboxMutation,
  usePauseMailboxMutation,
  useResumeMailboxMutation,
  useDeleteMailboxMutation,
  useStartGoogleOauthMutation,
  useStartMicrosoftOauthMutation,
} = mailboxApi
