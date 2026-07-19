import {
  createApi,
  fetchBaseQuery,
  type BaseQueryFn,
  type FetchArgs,
  type FetchBaseQueryError,
} from '@reduxjs/toolkit/query/react'
import { setSession, clearSession } from './slices/auth'

// The generated api.ts injects endpoints into this base. Never hand-edit api.ts.

/**
 * Shape of a login/register/refresh/switch-workspace response body, duplicated
 * from `slices/auth.ts` (rather than imported from the generated `api.ts`) so
 * this module never imports the generated API module. `api.ts` imports
 * `emptyApi` (this file) at runtime, so the reverse import here would close a
 * store<->api cycle; structural typing keeps the dependency one-way.
 */
type SessionResponse = {
  access_token: string
  expires_in: number
  user_id: string
  active_workspace_id: string
  role: string
  memberships: Array<{ workspace_id: string; workspace_name: string; role: string }>
}

const rawBaseQuery = fetchBaseQuery({
  // An absolute URL (rather than a bare '/api/v1') so relative-URL resolution
  // doesn't depend on a browser Document — jsdom in unit tests, and Node's
  // fetch/Request generally, have no notion of "the current page" to resolve
  // against. `window.location.origin` is always same-origin in prod (the SPA
  // and API share an origin behind the reverse proxy / Vite dev proxy).
  baseUrl: `${window.location.origin}/api/v1`,
  // Send the httpOnly refresh cookie + readable csrf_token cookie to every request.
  credentials: 'include',
  // Attach the in-memory access token (auth slice) as a Bearer token, and echo
  // the CSRF cookie back as a header (double-submit pattern) for state-changing
  // requests. Structural typing of getState avoids a store<->api import cycle.
  prepareHeaders: (headers, { getState }) => {
    const token = (getState() as { auth?: { accessToken?: string | null } }).auth?.accessToken
    if (token) headers.set('authorization', `Bearer ${token}`)
    const csrf = document.cookie
      .split('; ')
      .find((cookie) => cookie.startsWith('csrf_token='))
      ?.split('=')[1]
    if (csrf) headers.set('x-csrf-token', decodeURIComponent(csrf))
    return headers
  },
})

// Single-flight guard: concurrent 401s share one /auth/refresh call instead of
// each firing their own (which would race the refresh-token rotation).
let refreshing: Promise<Awaited<ReturnType<typeof rawBaseQuery>>> | null = null

/**
 * Wraps the raw base query with reauth-on-401: on a 401, refresh the session
 * once (via the httpOnly refresh cookie), replay the original request, and
 * fall back to clearing the session if the refresh itself fails.
 */
const baseQueryWithReauth: BaseQueryFn<string | FetchArgs, unknown, FetchBaseQueryError> = async (
  args,
  api,
  extra,
) => {
  let result = await rawBaseQuery(args, api, extra)

  if (result.error?.status === 401) {
    refreshing ??= (async () => {
      try {
        return await rawBaseQuery({ url: '/auth/refresh', method: 'POST' }, api, extra)
      } finally {
        refreshing = null
      }
    })()
    const refreshed = await refreshing

    if (refreshed.data) {
      api.dispatch(setSession(refreshed.data as SessionResponse))
      result = await rawBaseQuery(args, api, extra)
    } else {
      api.dispatch(clearSession())
    }
  }

  return result
}

export const emptyApi = createApi({
  reducerPath: 'api',
  baseQuery: baseQueryWithReauth,
  endpoints: () => ({}),
})
