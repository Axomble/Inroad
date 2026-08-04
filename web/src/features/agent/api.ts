import { api } from '@/store/api'

export const agentApi = api.enhanceEndpoints({
  addTagTypes: ['AgentThread', 'AgentThreadList', 'AgentQueue'],
  endpoints: {
    listAgentThreads: { providesTags: ['AgentThreadList'] },
    createAgentThread: { invalidatesTags: ['AgentThreadList'] },
    getAgentThread: { providesTags: ['AgentThread'] },
    renameAgentThread: { invalidatesTags: ['AgentThread', 'AgentThreadList'] },
    deleteAgentThread: { invalidatesTags: ['AgentThread', 'AgentThreadList'] },
    sendAgentMessage: { invalidatesTags: ['AgentThread', 'AgentThreadList', 'AgentQueue'] },
    listAgentQueue: { providesTags: ['AgentQueue'] },
    deleteAgentQueuedMessage: { invalidatesTags: ['AgentQueue', 'AgentThread'] },
  },
})

export const {
  useListAgentThreadsQuery,
  useCreateAgentThreadMutation,
  useGetAgentThreadQuery,
  useRenameAgentThreadMutation,
  useDeleteAgentThreadMutation,
  useSendAgentMessageMutation,
  useListAgentQueueQuery,
  useDeleteAgentQueuedMessageMutation,
  useStopAgentRunMutation,
} = agentApi

export { useListAiModelsQuery } from '@/store/api'

export type {
  AgentMessage,
  AgentPart,
  AgentThread,
  AgentQueuedMessage,
  AgentSendRequest,
  AgentBrowsingContext,
  AiModel,
} from '@/store/api'
