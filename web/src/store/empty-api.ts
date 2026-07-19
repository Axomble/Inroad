import { createApi, fetchBaseQuery } from '@reduxjs/toolkit/query/react'

// The generated api.ts injects endpoints into this base. Never hand-edit api.ts.
export const emptyApi = createApi({
  reducerPath: 'api',
  baseQuery: fetchBaseQuery({ baseUrl: '/api/v1' }),
  endpoints: () => ({}),
})
