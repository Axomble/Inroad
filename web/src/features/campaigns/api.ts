// Campaign feature endpoints. Tag wiring layered on top of the generated
// store/api.ts shapes via `enhanceEndpoints` so list invalidations happen
// automatically after any mutation.
//
// Cross-feature query-hook imports (mailboxes, lists) are allowed HERE as a
// deliberate loophole for read-only reference data that this feature's forms
// need in dropdowns — cross-feature UI imports remain forbidden.
import { api } from '@/store/api'

const campaignApi = api.enhanceEndpoints({
  addTagTypes: ['Campaign'],
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
    launchCampaign: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Campaign', id: arg.id },
        { type: 'Campaign', id: 'LIST' },
      ],
    },
  },
})

export const {
  useListCampaignsQuery,
  useGetCampaignQuery,
  useCreateCampaignMutation,
  useLaunchCampaignMutation,
} = campaignApi
