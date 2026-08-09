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
// A/B variant shapes come from the contract too, so the editor's draft state
// is typed by the same definition the API validates against.
export type { StepVariant, StepVariantRequest } from '@/store/api'
// The schedule shapes come from the contract too, so the editor's state is typed
// by the same definition the API validates against.
export type { CampaignSchedule, SendWindowDay, SendWindowInterval } from '@/store/api'
// Sender-pool shapes likewise: the panel's draft state is derived from the
// contract's types rather than re-declaring the row shape.
export type {
  CampaignSender,
  CampaignSenderPool,
  CampaignSenderPoolRequest,
  RotationMode,
} from '@/store/api'

// The campaign-scoped deliverability shapes. The workspace rollup lives in
// `features/deliverability/`, but these two endpoints are `/campaigns/{id}/…` and
// their tags belong with the rest of this campaign's, so the guardrails card is
// wired here rather than reaching across features.
export type { CampaignDeliverability, CampaignGuardrails, CampaignPauseEvent } from '@/store/api'

// Preflight + test-send shapes. Both endpoints are read-your-own-workspace
// actions with nothing else in the app to invalidate (a test send changes no
// resource the UI renders, and the preflight report is re-fetched fresh every
// time its dialog opens), so neither needs tag wiring below — they're
// re-exported as-is from the generated client, kept behind this feature's one
// import surface like everything else here.
export type { CampaignPreflight, CampaignPreflightCheck, TestSendRequest, TestSendResponse } from '@/store/api'

const campaignApi = api.enhanceEndpoints({
  addTagTypes: ['Campaign', 'Step', 'Schedule', 'SenderPool', 'Guardrails', 'Variant'],
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
    // Lifecycle transitions (rename/delete/pause/resume) all change what the
    // list row and the detail page show, so each mirrors launch's tag wiring:
    // invalidate both the specific campaign and the list.
    renameCampaign: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Campaign', id: arg.id },
        { type: 'Campaign', id: 'LIST' },
      ],
    },
    deleteCampaign: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Campaign', id: arg.id },
        { type: 'Campaign', id: 'LIST' },
      ],
    },
    pauseCampaign: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Campaign', id: arg.id },
        { type: 'Campaign', id: 'LIST' },
      ],
    },
    resumeCampaign: {
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
    // --- A/B variants ----------------------------------------------------
    //
    // Variants are tagged by their STEP, not by the campaign: the editor reads
    // one list per step, so a campaign-wide tag would refetch every step's
    // variants whenever one of them changed.
    //
    // The two weight writes additionally invalidate the campaign's Step tag,
    // because the step row itself carries variant_weight — without it, the base
    // side of the split would keep rendering its old share after a promotion.
    listStepVariants: {
      providesTags: (_result, _error, arg) => [{ type: 'Variant', id: arg.stepId }],
    },
    createStepVariant: {
      invalidatesTags: (_result, _error, arg) => [{ type: 'Variant', id: arg.stepId }],
    },
    updateStepVariant: {
      invalidatesTags: (_result, _error, arg) => [{ type: 'Variant', id: arg.stepId }],
    },
    deleteStepVariant: {
      invalidatesTags: (_result, _error, arg) => [{ type: 'Variant', id: arg.stepId }],
    },
    setStepBaseWeight: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Variant', id: arg.stepId },
        { type: 'Step', id: arg.id },
      ],
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
    // Same reasoning for the sender pool: its own tag so replacing the pool
    // doesn't refetch metrics, steps, or the schedule.
    getCampaignSenders: {
      providesTags: (_result, _error, arg) => [{ type: 'SenderPool', id: arg.id }],
    },
    updateCampaignSenders: {
      invalidatesTags: (_result, _error, arg) => [{ type: 'SenderPool', id: arg.id }],
    },
    // Deliverability and guardrails share one tag: saving a threshold can change
    // the verdict the same response carries, so the card refetches as a whole
    // rather than trusting the mutation's echo of the settings alone.
    getCampaignDeliverability: {
      providesTags: (_result, _error, arg) => [{ type: 'Guardrails', id: arg.id }],
    },
    updateCampaignGuardrails: {
      invalidatesTags: (_result, _error, arg) => [{ type: 'Guardrails', id: arg.id }],
    },
  },
})

export const {
  useListCampaignsQuery,
  useGetCampaignQuery,
  useCreateCampaignMutation,
  useLaunchCampaignMutation,
  useRenameCampaignMutation,
  useDeleteCampaignMutation,
  usePauseCampaignMutation,
  useResumeCampaignMutation,
  useUpdateCampaignTrackingMutation,
  useListCampaignEnrollmentsQuery,
  useListStepsQuery,
  useCreateStepMutation,
  useUpdateStepMutation,
  useDeleteStepMutation,
  useReorderStepsMutation,
  useListStepVariantsQuery,
  useCreateStepVariantMutation,
  useUpdateStepVariantMutation,
  useDeleteStepVariantMutation,
  useSetStepBaseWeightMutation,
  useGetCampaignScheduleQuery,
  useUpdateCampaignScheduleMutation,
  useGetCampaignSendersQuery,
  useUpdateCampaignSendersMutation,
  useGetCampaignDeliverabilityQuery,
  useUpdateCampaignGuardrailsMutation,
  useGetCampaignPreflightQuery,
  useTestSendCampaignMutation,
} = campaignApi
