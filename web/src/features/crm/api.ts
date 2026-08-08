// CRM feature endpoints. The generated `store/api.ts` already declares every
// CRM query/mutation from `api/openapi.yaml`; this module only layers cache
// tags (and the board's optimistic move) on top via `enhanceEndpoints` — the
// same pattern as features/ai-settings/api.ts.
//
// Nothing is injected here on purpose. A hand-injected `listCrmDeals` hitting
// the same URL as the generated `crmListDeals` is a *different* cache entry, so
// an optimistic patch written by one is invisible to the other — and because
// the names differ, RTK never warns. One definition per endpoint keeps the
// board and the lists reading the same bytes.
import { api } from '@/store/api'

// One source of truth for shapes: re-export the generated definitions rather
// than restating them.
export type {
  CrmCompany,
  CrmCompanyInput,
  CrmStage,
  CrmPipeline,
  CrmPipelineInput,
  CrmDeal,
  CrmDealInput,
  CrmBoard,
  CrmBoardStage,
  CrmMoveDealInput,
  CrmThread,
  CrmSettings,
  CrmCompanyContact,
} from '@/store/api'

import type { CrmDeal, CrmSettings } from '@/store/api'

/** The capture policy union, derived from the generated settings shape. */
export type AutoCapturePolicy = CrmSettings['auto_capture_policy']

const crmApi = api
  .enhanceEndpoints({
    // `RecordActivity` is declared by `features/records/api.ts` and repeated here
    // because moving a deal produces an activity event, and that feed belongs to
    // the records module. Tag types are one global namespace on the shared api
    // instance, so naming another module's tag is a cache dependency rather than
    // an import — nothing of that module's code comes with it.
    addTagTypes: ['CRMCompany', 'CRMPipeline', 'CRMDeal', 'CRMThread', 'CRMSettings', 'RecordActivity'],
    endpoints: {
      crmListCompanies: {
        providesTags: (result) =>
          result
            ? [...result.items.map(({ id }) => ({ type: 'CRMCompany' as const, id })), { type: 'CRMCompany', id: 'LIST' }]
            : [{ type: 'CRMCompany', id: 'LIST' }],
      },
      crmGetCompany: {
        providesTags: (_result, _error, { id }) => [{ type: 'CRMCompany', id }],
      },
      crmCreateCompany: {
        invalidatesTags: [{ type: 'CRMCompany', id: 'LIST' }],
      },
      crmUpdateCompany: {
        invalidatesTags: (_result, _error, { id }) => [{ type: 'CRMCompany', id }, { type: 'CRMCompany', id: 'LIST' }],
      },
      // Deleting a company unlinks its deals, so the deal list is stale too.
      crmDeleteCompany: {
        invalidatesTags: (_result, _error, { id }) => [
          { type: 'CRMCompany', id },
          { type: 'CRMCompany', id: 'LIST' },
          { type: 'CRMDeal', id: 'LIST' },
        ],
      },

      crmListPipelines: {
        providesTags: [{ type: 'CRMPipeline', id: 'LIST' }],
      },
      crmGetPipeline: {
        providesTags: (_result, _error, { id }) => [{ type: 'CRMPipeline', id }],
      },
      crmCreatePipeline: {
        invalidatesTags: [{ type: 'CRMPipeline', id: 'LIST' }],
      },
      crmUpdatePipeline: {
        invalidatesTags: (_result, _error, { id }) => [{ type: 'CRMPipeline', id }, { type: 'CRMPipeline', id: 'LIST' }],
      },
      crmDeletePipeline: {
        invalidatesTags: [{ type: 'CRMPipeline', id: 'LIST' }, { type: 'CRMDeal', id: 'LIST' }],
      },
      // Stage edits change the columns a deal is rendered in, so they take the
      // board and the deal list with them.
      crmCreateStage: {
        invalidatesTags: (_result, _error, { id }) => [{ type: 'CRMPipeline', id }, { type: 'CRMPipeline', id: 'LIST' }],
      },
      crmUpdateStage: {
        invalidatesTags: (_result, _error, { id }) => [
          { type: 'CRMPipeline', id },
          { type: 'CRMPipeline', id: 'LIST' },
          { type: 'CRMDeal', id: 'LIST' },
          { type: 'CRMDeal', id: 'BOARD' },
        ],
      },
      crmDeleteStage: {
        invalidatesTags: (_result, _error, { id }) => [
          { type: 'CRMPipeline', id },
          { type: 'CRMPipeline', id: 'LIST' },
          { type: 'CRMDeal', id: 'LIST' },
          { type: 'CRMDeal', id: 'BOARD' },
        ],
      },

      crmGetDeal: {
        providesTags: (_result, _error, { id }) => [{ type: 'CRMDeal', id }],
      },
      // A new deal changes the owning company's `deal_count`, so the company
      // list is invalidated alongside the deal list and the board.
      crmCreateDeal: {
        invalidatesTags: [
          { type: 'CRMDeal', id: 'LIST' },
          { type: 'CRMDeal', id: 'BOARD' },
          { type: 'CRMCompany', id: 'LIST' },
        ],
      },
      crmUpdateDeal: {
        invalidatesTags: (_result, _error, { id }) => [
          { type: 'CRMDeal', id },
          { type: 'CRMDeal', id: 'LIST' },
          { type: 'CRMDeal', id: 'BOARD' },
          { type: 'CRMCompany', id: 'LIST' },
        ],
      },
      crmDeleteDeal: {
        invalidatesTags: (_result, _error, { id }) => [
          { type: 'CRMDeal', id },
          { type: 'CRMDeal', id: 'LIST' },
          { type: 'CRMDeal', id: 'BOARD' },
          { type: 'CRMCompany', id: 'LIST' },
        ],
      },
      crmGetBoard: {
        providesTags: (result) =>
          result
            ? [
                ...result.stages.flatMap(({ deals }) => deals.map(({ id }) => ({ type: 'CRMDeal' as const, id }))),
                { type: 'CRMDeal', id: 'BOARD' },
              ]
            : [{ type: 'CRMDeal', id: 'BOARD' }],
      },
      crmMoveDeal: {
        // Drag-to-move has to feel instant, so the board is patched locally and
        // rolled back if the server refuses. The patch is applied to every
        // cached board arg (the board is per-pipeline) rather than a single
        // hard-coded `undefined` arg.
        async onQueryStarted({ id, crmMoveDealInput }, { dispatch, getState, queryFulfilled }) {
          const { stage_id: stageId, before_deal_id: beforeDealId, after_deal_id: afterDealId } = crmMoveDealInput
          const patches = crmApi.util
            .selectCachedArgsForQuery(getState(), 'crmGetBoard')
            .map((arg) =>
              dispatch(
                crmApi.util.updateQueryData('crmGetBoard', arg, (draft) => {
                  let moved: CrmDeal | undefined
                  for (const column of draft.stages) {
                    const index = column.deals.findIndex((deal) => deal.id === id)
                    if (index >= 0) {
                      ;[moved] = column.deals.splice(index, 1)
                      column.deal_count -= 1
                      column.amount_micros -= moved?.amount_micros ?? 0
                      break
                    }
                  }
                  const target = draft.stages.find(({ stage }) => stage.id === stageId)
                  if (!moved || !target) return
                  moved.stage_id = stageId
                  moved.stage_label = target.stage.label
                  moved.stage_color = target.stage.color
                  moved.stage_is_won = target.stage.is_won
                  moved.stage_is_lost = target.stage.is_lost
                  const beforeIndex = beforeDealId ? target.deals.findIndex((deal) => deal.id === beforeDealId) : -1
                  const afterIndex = afterDealId ? target.deals.findIndex((deal) => deal.id === afterDealId) : -1
                  const insertAt = afterIndex >= 0 ? afterIndex : beforeIndex >= 0 ? beforeIndex + 1 : target.deals.length
                  target.deals.splice(insertAt, 0, moved)
                  target.deal_count += 1
                  target.amount_micros += moved.amount_micros ?? 0
                }),
              ),
            )
          try {
            await queryFulfilled
          } catch {
            for (const patch of patches) patch.undo()
          }
        },
        // On success the patch already holds the new board; only the deal's own
        // caches (detail page, list, activity feed) need refetching. On failure
        // nothing changed server-side, so nothing is invalidated.
        invalidatesTags: (_result, error, { id }) =>
          error ? [] : [{ type: 'CRMDeal', id }, { type: 'CRMDeal', id: 'LIST' }, { type: 'RecordActivity', id }],
      },

      // Conversation context is deal-only (campaign threads linked to a deal), so
      // unlike notes/tasks/activity it stays here.
      crmListDealThreads: {
        providesTags: (_result, _error, { id }) => [{ type: 'CRMThread', id }],
      },

      // A company's roster and its deals are genuinely unbounded, so they are
      // sub-resources rather than embedded. The deals page also carries the deal
      // LIST tag, so creating, moving or deleting a deal refreshes it.
      crmListCompanyContacts: {
        providesTags: (_result, _error, { id }) => [{ type: 'CRMCompany', id }],
      },
      crmListCompanyDeals: {
        providesTags: (_result, _error, { id }) => [
          { type: 'CRMCompany', id },
          { type: 'CRMDeal', id: 'LIST' },
        ],
      },

      crmGetSettings: {
        providesTags: [{ type: 'CRMSettings', id: 'WORKSPACE' }],
      },
      crmUpdateSettings: {
        invalidatesTags: [{ type: 'CRMSettings', id: 'WORKSPACE' }],
      },
    },
  })

export const {
  useCrmListCompaniesQuery,
  useCrmGetCompanyQuery,
  useCrmCreateCompanyMutation,
  useCrmListCompanyContactsQuery,
  useCrmListCompanyDealsQuery,
  useCrmListPipelinesQuery,
  useCrmCreatePipelineMutation,
  useCrmCreateDealMutation,
  useCrmGetDealQuery,
  useCrmGetBoardQuery,
  useCrmMoveDealMutation,
  useCrmListDealThreadsQuery,
  useCrmGetSettingsQuery,
  useCrmUpdateSettingsMutation,
} = crmApi
