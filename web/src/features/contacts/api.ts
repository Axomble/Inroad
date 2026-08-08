// Contacts/lists feature endpoints. Adds cache tags via `enhanceEndpoints`, and
// overrides the generated importContacts endpoint via `injectEndpoints` so the
// CSV file goes over the wire as a real `FormData` body (fetchBaseQuery treats
// plain objects as JSON, which would `JSON.stringify` the File to "{}").
// Because we go through the shared baseQuery, reauth-on-401 still works.
import { api } from '@/store/api'
import type { ImportResult } from '@/store/api'

const contactsApi = api
  .enhanceEndpoints({
    // `CRMCompany` is declared by `features/crm/api.ts` and repeated here because
    // linking a contact to a company changes that company's roster. Tag types are
    // one global namespace on the shared api instance, so naming another module's
    // tag is a cache dependency, not an import — none of its code comes with it.
    addTagTypes: ['List', 'Contact', 'ContactEngagement', 'CRMCompany'],
    endpoints: {
      listLists: {
        providesTags: (result) =>
          result
            ? [
                ...result.map((l) => ({ type: 'List' as const, id: l.id ?? 'unknown' })),
                { type: 'List' as const, id: 'LIST' },
              ]
            : [{ type: 'List' as const, id: 'LIST' }],
      },
      createList: {
        invalidatesTags: [{ type: 'List', id: 'LIST' }],
      },
      renameList: {
        invalidatesTags: [{ type: 'List', id: 'LIST' }],
      },
      // Also drops the contacts cache: the deleted list's members leave the
      // all-contacts scope semantics unchanged, but a view still filtered to it
      // must not replay cached rows for a list that no longer exists.
      deleteList: {
        invalidatesTags: [
          { type: 'List', id: 'LIST' },
          { type: 'Contact', id: 'LIST' },
        ],
      },
      // One coarse tag for every page: `list` is now optional, and a page is
      // also keyed by q/sort/cursor/limit, so per-list tags would leave the
      // all-contacts view and every other search stale after an import.
      listContacts: {
        providesTags: [{ type: 'Contact', id: 'LIST' }],
      },
      getContact: {
        providesTags: (_result, _error, { id }) => [{ type: 'Contact', id }],
      },
      // The engagement rollup is deliberately a second request: the detail read is
      // two index seeks, this one is four aggregates, so keeping them apart lets
      // the record header paint without waiting on the aggregates.
      //
      // It carries its own tag rather than sharing the contact's: linking a
      // company changes the record but cannot change a single send, open or
      // reply, and one shared tag would re-run all four aggregates for it.
      getContactEngagement: {
        providesTags: (_result, _error, { id }) => [{ type: 'ContactEngagement', id }],
      },
      // One write, four stale reads, handled three different ways.
      //
      // The contact's own record is *written from the response* — the endpoint
      // returns the whole updated `ContactDetail`, so refetching it would be a
      // round trip for bytes we already hold. This runs on success only; a failed
      // link leaves the cache untouched, so the UI can never claim a link the
      // server refused.
      //
      // The contacts list is invalidated because its rows carry `company_name`.
      //
      // Both company rosters — the one joined and the one left — are covered by
      // invalidating the whole `CRMCompany` family. The *previous* company is in
      // neither the arguments nor the response (only the caller knew it), and at
      // most one company page is mounted at a time, so a family invalidation costs
      // one refetch and cannot miss the old roster.
      setContactCompany: {
        async onQueryStarted({ id }, { dispatch, queryFulfilled }) {
          try {
            const { data } = await queryFulfilled
            dispatch(contactsApi.util.upsertQueryData('getContact', { id }, data))
          } catch {
            // Deliberately swallowed, and it is not an unhandled error: the
            // component that triggered the mutation reads the failure off the
            // result and shows it. This handler exists only to seed the cache
            // with the record the response already returned, so on failure
            // there is simply nothing to seed — and `invalidatesTags` still
            // refetches, so nothing goes stale.
            //
            // The `catch` itself is load-bearing. An awaited `queryFulfilled`
            // that nobody observes becomes an unhandled promise rejection: it
            // fired on every failed link in the browser, and in CI it made
            // vitest exit non-zero while every single test passed.
          }
        },
        invalidatesTags: [{ type: 'Contact', id: 'LIST' }, 'CRMCompany'],
      },
    },
  })
  .injectEndpoints({
    endpoints: (build) => ({
      importContactsCsv: build.mutation<ImportResult, { list: string; file: File }>({
        query: ({ list, file }) => {
          const body = new FormData()
          body.append('file', file)
          return { url: '/contacts/import', method: 'POST', body, params: { list } }
        },
        invalidatesTags: [{ type: 'Contact', id: 'LIST' }],
      }),
    }),
    overrideExisting: false,
  })

// One source of truth for shapes: re-export the generated definitions rather
// than restating them.
export type {
  ContactDetail,
  ContactSuppression,
  ContactDeal,
  ContactEngagement,
  ContactCampaignEnrollment,
  ContactCompany,
  ContactCompanyLink,
} from '@/store/api'

export const {
  useGetContactQuery,
  useSetContactCompanyMutation,
  useGetContactEngagementQuery,
  useListListsQuery,
  useCreateListMutation,
  useRenameListMutation,
  useDeleteListMutation,
  useListContactsQuery,
  useImportContactsCsvMutation,
} = contactsApi
