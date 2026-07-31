// Campaign feature endpoints. Tag wiring layered on top of the generated
// store/api.ts shapes via `enhanceEndpoints` so list invalidations happen
// automatically after any mutation. The enrollments listing lives in the
// generated client too (from the OpenAPI path); we only attach its providesTags
// here.
//
// The reorder endpoint (`POST /campaigns/{id}/steps/reorder`) was originally to
// be injected here contract-first, but the backend's `gen:api` has since landed
// it in the generated store/api.ts (`reorderSteps`, arg
// `{ id, reorderStepsRequest: { step_ids } }` → `SequenceStep[]`). We now use
// the generated endpoint directly and only attach its tag wiring below.
//
// Cross-feature query-hook imports (mailboxes, lists) are allowed HERE as a
// deliberate loophole for read-only reference data that this feature's forms
// need in dropdowns — cross-feature UI imports remain forbidden.
import { api } from '@/store/api'

// Re-export the generated enrollment type so feature components import it from
// the feature barrel rather than reaching into store/api.ts directly. The
// generated shape carries a strict `reply_class` union.
export type { CampaignEnrollment } from '@/store/api'
// Step shapes are generated too; re-export so the sequence editor derives its
// form/card types from the contract rather than hand-duplicating them.
export type { SequenceStep, StepRequest } from '@/store/api'
// The schedule shapes come from the contract too, so the editor's state is typed
// by the same definition the API validates against.
export type { CampaignSchedule, SendWindowDay, SendWindowInterval } from '@/store/api'

const campaignApi = api.enhanceEndpoints({
  addTagTypes: ['Campaign', 'Step', 'Schedule'],
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
    // No `providesTags` for enrollments: replies are classified server-side by
    // the worker, so no client mutation can ever invalidate an `Enrollment`
    // tag — it would be a dead tag that never refreshes. The component instead
    // sets `refetchOnMountOrArgChange` so reopening the detail (or paging)
    // pulls fresh reply classes without continuous polling.
    // Sequence steps: one `Step` tag keyed by campaign id. Every structural or
    // content mutation invalidates it so the ordered list refetches.
    listSteps: {
      providesTags: (_result, _error, arg) => [{ type: 'Step', id: arg.id }],
    },
    createStep: {
      invalidatesTags: (_result, _error, arg) => [{ type: 'Step', id: arg.id }],
    },
    updateStep: {
      invalidatesTags: (_result, _error, arg) => [{ type: 'Step', id: arg.id }],
    },
    deleteStep: {
      invalidatesTags: (_result, _error, arg) => [{ type: 'Step', id: arg.id }],
    },
    reorderSteps: {
      invalidatesTags: (_result, _error, arg) => [{ type: 'Step', id: arg.id }],
    },
    // The sending schedule gets its own tag rather than reusing `Campaign`: a
    // schedule save shouldn't refetch metrics and steps, and a campaign mutation
    // shouldn't refetch the schedule.
    getCampaignSchedule: {
      providesTags: (_result, _error, arg) => [{ type: 'Schedule', id: arg.id }],
    },
    updateCampaignSchedule: {
      invalidatesTags: (_result, _error, arg) => [{ type: 'Schedule', id: arg.id }],
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
  useListStepsQuery,
  useCreateStepMutation,
  useUpdateStepMutation,
  useDeleteStepMutation,
  useReorderStepsMutation,
  useGetCampaignScheduleQuery,
  useUpdateCampaignScheduleMutation,
} = campaignApi
