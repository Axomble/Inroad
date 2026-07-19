import { createApi, fetchBaseQuery } from '@reduxjs/toolkit/query/react'

// The generated api.ts injects endpoints into this base. Never hand-edit api.ts.
export const emptyApi = createApi({
  reducerPath: 'api',
  baseQuery: fetchBaseQuery({
    baseUrl: '/api/v1',
    // Attach the first-party JWT (stored in the auth slice) as a Bearer token.
    // Structural typing of getState avoids a store<->api import cycle.
    prepareHeaders: (headers, { getState }) => {
      const token = (getState() as { auth?: { token?: string | null } }).auth?.token
      if (token) headers.set('authorization', `Bearer ${token}`)
      return headers
    },
  }),
  endpoints: () => ({}),
})
