// AI settings feature endpoints. The generated store/api.ts declares the raw
// query/mutation shapes (final agent-platform A1 contract); here we layer
// cache tags on top via `enhanceEndpoints` so list invalidations happen
// automatically after any mutation — the same pattern as
// features/mailboxes/api.ts. Nothing is injected anymore: the earlier
// Workspace-infixed injected endpoints existed only to dodge a name collision
// with a stale generated snapshot, and were deleted at reconciliation.
import { api } from '@/store/api'

// One source of truth for shapes: re-export the generated definitions rather
// than restating them.
export type {
  AiSettings,
  AiSettingsUpdate,
  AiProvider,
  AiProviderKind,
  AiProviderConfig,
  AiProviderCredentials,
  AiProviderCreateRequest,
  AiProviderUpdateRequest,
  AiDiscoveredModel,
  AiDiscoveryResult,
  AiModel,
  AiModelCreateRequest,
} from '@/store/api'

/**
 * Sentinel model ids the backend resolves to its recommended default at run
 * time; the UI renders them as "Auto (recommended)".
 */
export const DEFAULT_SMART_MODEL = 'default-smart-model'
export const DEFAULT_FAST_MODEL = 'default-fast-model'

const aiSettingsApi = api.enhanceEndpoints({
  addTagTypes: ['AiSettings', 'AiProviders', 'AiModels'],
  endpoints: {
    getAiSettings: {
      providesTags: ['AiSettings'],
    },
    updateAiSettings: {
      invalidatesTags: ['AiSettings'],
    },
    listAiProviders: {
      providesTags: ['AiProviders'],
    },
    listAiModels: {
      providesTags: ['AiModels'],
    },
    // Adding, editing, or removing a provider changes which models exist (a
    // deleted row takes its models with it; discovery targets change), so
    // provider mutations invalidate the model list too.
    createAiProvider: {
      invalidatesTags: ['AiProviders', 'AiModels'],
    },
    updateAiProvider: {
      invalidatesTags: ['AiProviders', 'AiModels'],
    },
    deleteAiProvider: {
      invalidatesTags: ['AiProviders', 'AiModels'],
    },
    createAiModel: {
      invalidatesTags: ['AiModels'],
    },
    deleteAiModel: {
      invalidatesTags: ['AiModels'],
    },
    // discoverAiProviderModels returns candidates only — nothing persists, so
    // nothing invalidates.
  },
})

export const {
  useGetAiSettingsQuery,
  useUpdateAiSettingsMutation,
  useListAiProvidersQuery,
  useListAiModelsQuery,
  useCreateAiProviderMutation,
  useUpdateAiProviderMutation,
  useDeleteAiProviderMutation,
  useDiscoverAiProviderModelsMutation,
  useCreateAiModelMutation,
  useDeleteAiModelMutation,
} = aiSettingsApi
