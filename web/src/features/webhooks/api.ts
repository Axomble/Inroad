// Outbound webhook endpoints and their delivery log. The generated
// store/api.ts declares the raw query/mutation shapes from the OpenAPI
// contract; the cache tags are layered on here via `enhanceEndpoints` (mirrors
// features/dead-letters/api.ts and features/mailboxes/api.ts) so an action
// refetches whatever it changed without a hand-rolled `refetch()` in a
// component.
import { api } from '@/store/api'

const webhookApi = api
  .enhanceEndpoints({
    addTagTypes: ['WebhookEndpoint', 'WebhookDelivery'],
    endpoints: {
      listWebhookEndpoints: {
        // One LIST tag, not one per endpoint: nothing on this screen subscribes
        // to a single endpoint's detail view (no `getWebhookEndpoint` call —
        // the row renders straight off the list item), so a per-id tag would be
        // a cache dependency nothing ever reads.
        providesTags: [{ type: 'WebhookEndpoint', id: 'LIST' }],
      },
      createWebhookEndpoint: {
        invalidatesTags: [{ type: 'WebhookEndpoint', id: 'LIST' }],
      },
      deleteWebhookEndpoint: {
        invalidatesTags: [{ type: 'WebhookEndpoint', id: 'LIST' }],
      },
      // rotateWebhookEndpointSecret invalidates nothing. It changes only the
      // secret, and every field the list actually renders (url, description,
      // event_types, active, created_at) is untouched, so refetching the list
      // would be a round trip for zero visible change.
      //
      // The secret is not in the QUERY cache, but it is not "never cached"
      // either: RTK Query mirrors every mutation's `data` into
      // `state[api.reducerPath].mutations[...]`, so the response sits there
      // until the caller resets it or the component holding the hook unmounts.
      // The `api` reducer is not persisted (store/index.ts whitelists UI slices
      // only), so this is not a live leak — but WebhookEndpointRow stays
      // mounted for the life of the settings page and therefore calls `reset()`
      // explicitly when the reveal is dismissed.
      listWebhookDeliveries: {
        // Scoped per endpoint id rather than one shared LIST: an operator can
        // have two endpoints' delivery logs open at once, and a shared tag
        // would refetch — and silently reset the pager of — a log they never
        // touched just because a sibling endpoint got pinged.
        providesTags: (_result, _error, arg) => [{ type: 'WebhookDelivery', id: arg.id }],
      },
      pingWebhookEndpoint: {
        // Ping's entire purpose is to produce a new delivery row, so its own
        // endpoint's log tag is invalidated — an open log picks up the
        // synthetic ping without a manual refresh.
        invalidatesTags: (_result, _error, arg) => [{ type: 'WebhookDelivery', id: arg.id }],
      },
    },
  })

export const {
  useListWebhookEndpointsQuery,
  useCreateWebhookEndpointMutation,
  useDeleteWebhookEndpointMutation,
  useRotateWebhookEndpointSecretMutation,
  usePingWebhookEndpointMutation,
  useListWebhookDeliveriesQuery,
} = webhookApi
