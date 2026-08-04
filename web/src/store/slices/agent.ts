import { createSlice, type PayloadAction } from '@reduxjs/toolkit'
import type { AgentMessage, AgentQueuedMessage } from '@/store/api'

export interface StreamingPart {
  id: string
  order_index: number
  type: 'text' | 'reasoning' | 'tool_call'
  text?: string
  reasoning?: string
  tool_name?: string
  tool_call_id?: string
  tool_input?: unknown
  tool_output?: unknown
  loading_message?: string
  state?: 'running' | 'done' | 'error'
  error?: string
}

export interface StreamingMessage {
  id: string
  runId: string
  createdAt: string
  parts: StreamingPart[]
}

interface AgentState {
  activeThreadId: string | null
  messageIds: string[]
  messagesById: Record<string, AgentMessage>
  queued: AgentQueuedMessage[]
  streaming: StreamingMessage | null
  streamStatus: 'idle' | 'connecting' | 'running' | 'error'
  streamError: string | null
  lastEventIds: Record<string, number>
  submittedDraft: string
  restoredDraft: string
}

export const initialAgentState: AgentState = {
  activeThreadId: null,
  messageIds: [],
  messagesById: {},
  queued: [],
  streaming: null,
  streamStatus: 'idle',
  streamError: null,
  lastEventIds: {},
  submittedDraft: '',
  restoredDraft: '',
}

function normalizedMessages(messages: AgentMessage[]): AgentMessage[] {
  const results = new Map<string, AgentMessage['parts'][number]>()
  for (const message of messages) {
    for (const part of message.parts) {
      if (part.type === 'tool_result' && part.tool_call_id) results.set(part.tool_call_id, part)
    }
  }
  return messages
    .filter(
      (message) =>
        message.status !== 'queued' &&
        !(message.role === 'user' && message.parts.every((part) => part.type === 'tool_result')),
    )
    .map((message) => Object.assign({}, message, {
      parts: message.parts.map((part) => {
        if (part.type !== 'tool_call' || !part.tool_call_id) return part
        const result = results.get(part.tool_call_id)
        return result
          ? {
              ...part,
              tool_output: result.tool_output,
              state: result.state === 'error' ? 'error' : 'done',
              error: result.error,
            }
          : part
      }),
    }))
}

const agentSlice = createSlice({
  name: 'agent',
  initialState: initialAgentState,
  reducers: {
    selectAgentThread: (state, action: PayloadAction<string | null>) => {
      if (state.activeThreadId === action.payload) return
      state.activeThreadId = action.payload
      state.messageIds = []
      state.messagesById = {}
      state.queued = []
      state.streaming = null
      state.streamStatus = action.payload ? 'connecting' : 'idle'
      state.streamError = null
    },
    replaceAgentMessages: (state, action: PayloadAction<AgentMessage[]>) => {
      const visible = normalizedMessages(action.payload)
      state.messageIds = visible.map((message) => message.id)
      state.messagesById = Object.fromEntries(visible.map((message) => [message.id, message]))
    },
    setAgentQueue: (state, action: PayloadAction<AgentQueuedMessage[]>) => {
      state.queued = action.payload
    },
    setStreamingMessage: (state, action: PayloadAction<StreamingMessage | null>) => {
      state.streaming = action.payload
      if (action.payload) state.streamStatus = 'running'
    },
    setAgentStreamStatus: (state, action: PayloadAction<AgentState['streamStatus']>) => {
      state.streamStatus = action.payload
      if (action.payload !== 'error') state.streamError = null
    },
    setAgentStreamError: (state, action: PayloadAction<string>) => {
      state.streamStatus = 'error'
      state.streamError = action.payload
      state.restoredDraft = state.submittedDraft
      state.submittedDraft = ''
    },
    setLastAgentEventId: (
      state,
      action: PayloadAction<{ threadId: string; eventId: number }>,
    ) => {
      state.lastEventIds[action.payload.threadId] = action.payload.eventId
    },
    setSubmittedAgentDraft: (state, action: PayloadAction<string>) => {
      state.submittedDraft = action.payload
      state.restoredDraft = ''
    },
    clearSubmittedAgentDraft: (state) => {
      state.submittedDraft = ''
    },
    clearRestoredAgentDraft: (state) => {
      state.restoredDraft = ''
    },
  },
})

export const {
  selectAgentThread,
  replaceAgentMessages,
  setAgentQueue,
  setStreamingMessage,
  setAgentStreamStatus,
  setAgentStreamError,
  setLastAgentEventId,
  setSubmittedAgentDraft,
  clearSubmittedAgentDraft,
  clearRestoredAgentDraft,
} = agentSlice.actions

export default agentSlice.reducer
