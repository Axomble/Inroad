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

// Domain-authentication shapes come from the contract; re-export so the panel
// derives its types from the generated definition rather than restating it.
export type { SendingDomain, DomainAuthState, SpfStatus, DmarcStatus, DkimStatus } from '@/store/api'

const mailboxApi = api.enhanceEndpoints({
  addTagTypes: ['Mailbox', 'SendingDomain'],
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
    // Sending domains are derived from the workspace's mailboxes, so connecting
    // or deleting one can add or remove a domain row.
    connectMailbox: {
      invalidatesTags: [
        { type: 'Mailbox', id: 'LIST' },
        { type: 'SendingDomain', id: 'LIST' },
      ],
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
        { type: 'SendingDomain', id: 'LIST' },
      ],
    },
    listSendingDomains: {
      providesTags: (result) =>
        result
          ? [
              ...result.map((d) => ({ type: 'SendingDomain' as const, id: d.domain })),
              { type: 'SendingDomain' as const, id: 'LIST' },
            ]
          : [{ type: 'SendingDomain' as const, id: 'LIST' }],
    },
    // A recheck returns the updated row, but the list is what's on screen —
    // invalidate it so the row the operator just rechecked actually updates.
    checkSendingDomain: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'SendingDomain', id: arg.domain },
        { type: 'SendingDomain', id: 'LIST' },
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
  useListSendingDomainsQuery,
  useCheckSendingDomainMutation,
} = mailboxApi
