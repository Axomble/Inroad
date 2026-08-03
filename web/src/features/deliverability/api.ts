// Deliverability feature endpoints. The generated store/api.ts already declares
// the query shapes from the OpenAPI contract; here we only layer cache tags via
// `enhanceEndpoints` (the generated file is never hand-edited).
//
// Scope is deliberately the WORKSPACE rollup only. The two campaign-scoped
// endpoints (`GET /campaigns/{id}/deliverability`, `PUT /campaigns/{id}/guardrails`)
// are wired in `features/campaigns/api.ts` alongside the rest of that campaign's
// tags — features may not import each other, and the guardrails card lives with
// the campaign detail it belongs to.
//
// `POST /deliverability/events` is intentionally not surfaced here: it is an
// API-key-authenticated machine endpoint for an external provider feed, not a
// browser action.
import { api } from '@/store/api'

// Re-export the contract's shapes so feature components derive their types from
// the generated definition rather than re-declaring the rows.
export type {
  AtRiskItem,
  DeliverabilityPoint,
  DeliverabilityReport,
  DeliverabilityScore,
  ScoreComponent,
} from '@/store/api'

const deliverabilityApi = api.enhanceEndpoints({
  addTagTypes: ['Deliverability'],
  endpoints: {
    getWorkspaceDeliverability: {
      providesTags: [{ type: 'Deliverability', id: 'WORKSPACE' }],
    },
  },
})

export const { useGetWorkspaceDeliverabilityQuery } = deliverabilityApi
