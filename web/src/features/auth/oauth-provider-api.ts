// OAuth 2.1 provider (authorization-server) endpoints for the SPA — the consent
// screen and the admin "connected apps" management surface.
//
// WHY THIS FILE EXISTS (reconciling generated vs hand-injected endpoints):
// The backend mounts the provider at the API server ROOT (`/oauth2/*`), a SIBLING
// of the `/api/v1` data plane (see cmd/inroad/main.go — a public `/oauth2` mount on
// the router root). The OpenAPI marks each path with `servers: [{ url: / }]` to say
// so, but rtk-query-codegen-openapi ignores per-operation servers, so the generated
// `oauth2*` endpoints resolve against the `/api/v1` base query and would hit
// `/api/v1/oauth2/*` (404). We therefore re-inject them here with an absolute root
// URL. The request/response TYPES still come from the generated client (one source
// of truth); only the URL is corrected, and cache tags are added.
import { api } from '@/store/api'
import type {
  Oauth2ConsentDataApiResponse,
  Oauth2ConsentDataApiArg,
  Oauth2ConsentDecideApiResponse,
  Oauth2ConsentDecideApiArg,
  Oauth2ListClientsApiResponse,
  Oauth2ListClientsApiArg,
  Oauth2RegisterApiResponse,
  Oauth2RegisterApiArg,
  Oauth2RevokeClientApiResponse,
  Oauth2RevokeClientApiArg,
} from '@/store/api'

/**
 * Absolute URL for a provider path mounted at the API server root. The base query
 * targets `VITE_API_BASE_URL ?? '/api/v1'`; the provider lives one level up, at the
 * same host's root, so we resolve the API base against the page origin and hang the
 * `/oauth2/*` path off that origin. Returning an absolute (scheme-qualified) URL
 * makes fetchBaseQuery treat it as absolute and skip the `/api/v1` join, while the
 * shared `prepareHeaders` still attaches the Bearer token + `X-CSRF-Token` and
 * `credentials: 'include'` still ships the session/CSRF cookies.
 */
function providerUrl(path: string): string {
  const apiBase = import.meta.env.VITE_API_BASE_URL ?? '/api/v1'
  const origin = new URL(apiBase, window.location.origin).origin
  return `${origin}${path}`
}

const oauthProviderApi = api
  .enhanceEndpoints({ addTagTypes: ['OAuthClients'] })
  .injectEndpoints({
    overrideExisting: true,
    endpoints: (build) => ({
      // Consent display data. Session-authed; 404 when the request is unknown,
      // expired, consumed, or not this user's (a clear invalid/expired screen).
      oauth2ConsentData: build.query<Oauth2ConsentDataApiResponse, Oauth2ConsentDataApiArg>({
        query: ({ consentId }) => ({ url: providerUrl(`/oauth2/consent/${consentId}`) }),
      }),
      // Approve / deny. CSRF-guarded (double-submit header from the base query).
      // Returns the external `redirect_to` the SPA navigates the browser to.
      oauth2ConsentDecide: build.mutation<Oauth2ConsentDecideApiResponse, Oauth2ConsentDecideApiArg>({
        query: ({ oAuth2ConsentDecision }) => ({
          url: providerUrl('/oauth2/consent'),
          method: 'POST',
          body: oAuth2ConsentDecision,
        }),
      }),
      // Admin, workspace-scoped list of registered clients. Registering / revoking
      // invalidates the list tag so the panel refetches itself.
      oauth2ListClients: build.query<Oauth2ListClientsApiResponse, Oauth2ListClientsApiArg>({
        query: () => ({ url: providerUrl('/oauth2/clients') }),
        providesTags: [{ type: 'OAuthClients', id: 'LIST' }],
      }),
      oauth2Register: build.mutation<Oauth2RegisterApiResponse, Oauth2RegisterApiArg>({
        query: ({ oAuth2RegisterRequest }) => ({
          url: providerUrl('/oauth2/register'),
          method: 'POST',
          body: oAuth2RegisterRequest,
        }),
        invalidatesTags: [{ type: 'OAuthClients', id: 'LIST' }],
      }),
      oauth2RevokeClient: build.mutation<Oauth2RevokeClientApiResponse, Oauth2RevokeClientApiArg>({
        query: ({ clientId }) => ({
          url: providerUrl(`/oauth2/clients/${clientId}`),
          method: 'DELETE',
        }),
        invalidatesTags: [{ type: 'OAuthClients', id: 'LIST' }],
      }),
    }),
  })

export const {
  useOauth2ConsentDataQuery,
  useOauth2ConsentDecideMutation,
  useOauth2ListClientsQuery,
  useOauth2RegisterMutation,
  useOauth2RevokeClientMutation,
} = oauthProviderApi
