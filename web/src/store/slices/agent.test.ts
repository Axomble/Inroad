import reducer, {
  initialAgentState,
  replaceAgentMessages,
  selectAgentThread,
  setAgentStreamError,
  setSubmittedAgentDraft,
} from './agent'
import type { AgentMessage } from '@/store/api'

describe('agent state', () => {
  it('normalizes persisted tool results into their assistant tool call', () => {
    const messages: AgentMessage[] = [
      {
        id: 'assistant-1', turn_id: 'turn-1', role: 'assistant', status: 'sent', created_at: '2026-08-05T10:00:00Z',
        parts: [{ id: 'part-1', order_index: 0, type: 'tool_call', tool_call_id: 'call-1', tool_name: 'inroad_lookup', state: 'running' }],
      },
      {
        id: 'result-1', turn_id: 'turn-1', role: 'user', status: 'sent', created_at: '2026-08-05T10:00:01Z',
        parts: [{ id: 'part-2', order_index: 0, type: 'tool_result', tool_call_id: 'call-1', tool_output: { found: true }, state: 'done' }],
      },
    ]

    const state = reducer(initialAgentState, replaceAgentMessages(messages))

    expect(state.messageIds).toEqual(['assistant-1'])
    expect(state.messagesById['assistant-1']?.parts[0]).toMatchObject({
      type: 'tool_call',
      state: 'done',
      tool_output: { found: true },
    })
  })

  it('restores the submitted draft when a run fails', () => {
    let state = reducer(initialAgentState, selectAgentThread('thread-1'))
    state = reducer(state, setSubmittedAgentDraft('Check my campaign'))
    state = reducer(state, setAgentStreamError('Provider failed'))

    expect(state.restoredDraft).toBe('Check my campaign')
    expect(state.submittedDraft).toBe('')
    expect(state.streamStatus).toBe('error')
  })
})
