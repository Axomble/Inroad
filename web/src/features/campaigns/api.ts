// Campaign feature endpoints. Tag wiring layered on top of the generated
// store/api.ts shapes via `enhanceEndpoints` so list invalidations happen
// automatically after any mutation. The enrollments listing lives in the
// generated client too (from the OpenAPI path); we only attach its providesTags
// here.
//
// Cross-feature query-hook imports (mailboxes, lists) are allowed HERE as a
// deliberate loophole for read-only reference data that this feature's forms
// need in dropdowns — cross-feature UI imports remain forbidden.
import { api } from '@/store/api'

// Re-export the generated enrollment type so feature components import it from
// the feature barrel rather than reaching into store/api.ts directly. The
// generated shape carries a strict `reply_class` union.
export type { CampaignEnrollment } from '@/store/api'

const campaignApi = api.enhanceEndpoints({
  addTagTypes: ['Campaign', 'Enrollment'],
  endpoints: {
    listCampaigns: {
      providesTags: (result) =>
        result
          ? [
              ...result.map((c) => ({ type: 'Campaign' as const, id: c.id ?? 'unknown' })),
              { type: 'Campaign' as const, id: 'LIST' },
            ]
          : [{ type: 'Campaign' as const, id: 'LIST' }],
    },
    getCampaign: {
      providesTags: (_result, _error, arg) => [{ type: 'Campaign', id: arg.id }],
    },
    createCampaign: {
      invalidatesTags: [{ type: 'Campaign', id: 'LIST' }],
    },
    // The tracking-toggle response only echoes `tracking_enabled` — invalidate
    // the detail tag so the rest of the campaign view (metrics, stats) refetches.
    updateCampaignTracking: {
      invalidatesTags: (_result, _error, arg) => [{ type: 'Campaign', id: arg.id }],
    },
    launchCampaign: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Campaign', id: arg.id },
        { type: 'Campaign', id: 'LIST' },
      ],
    },
    listCampaignEnrollments: {
      providesTags: (_result, _error, arg) => [{ type: 'Enrollment', id: arg.id }],
    },
  },
})

export const {
  useListCampaignsQuery,
  useGetCampaignQuery,
  useCreateCampaignMutation,
  useLaunchCampaignMutation,
  useUpdateCampaignTrackingMutation,
  useListCampaignEnrollmentsQuery,
} = campaignApi
