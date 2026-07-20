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
      listContacts: {
        providesTags: (_result, _error, arg) => [{ type: 'Contact', id: `LIST-${arg.list}` }],
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
        invalidatesTags: (_result, _error, arg) => [{ type: 'Contact', id: `LIST-${arg.list}` }],
      }),
    }),
    overrideExisting: false,
  })

export const {
  useListListsQuery,
  useCreateListMutation,
  useListContactsQuery,
  useImportContactsCsvMutation,
} = contactsApi
