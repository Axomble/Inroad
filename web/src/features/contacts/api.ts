// Contacts/lists feature endpoints. Adds cache tags via `enhanceEndpoints`, and
// overrides the generated importContacts endpoint via `injectEndpoints` so the
// CSV file goes over the wire as a real `FormData` body (fetchBaseQuery treats
// plain objects as JSON, which would `JSON.stringify` the File to "{}").
// Because we go through the shared baseQuery, reauth-on-401 still works.
import { api } from '@/store/api'
import type { ImportResult } from '@/store/api'

const contactsApi = api
  .enhanceEndpoints({
    addTagTypes: ['List', 'Contact'],
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

export const {
  useListListsQuery,
  useCreateListMutation,
  useRenameListMutation,
  useDeleteListMutation,
  useListContactsQuery,
  useImportContactsCsvMutation,
} = contactsApi
