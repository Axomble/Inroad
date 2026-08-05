import type { AgentQueuedMessage } from './api'
import type { StreamingMessage, StreamingPart } from '@/store/slices/agent'

export interface AgentStreamEvent {
  type:
    | 'text_delta'
    | 'reasoning_delta'
    | 'tool_input_start'
    | 'tool_input_delta'
    | 'tool_output'
    | 'approval_required'
    | 'thread_title'
    | 'queue_updated'
    | 'message_persisted'
    | 'run_error'
    | 'done'
  run_id?: string
  text?: string
  tool_call_id?: string
  tool_name?: string
  tool_input?: unknown
  tool_output?: unknown
  is_error?: boolean
  loading_message?: string
  title?: string
  queued?: AgentQueuedMessage[]
  object_types?: string[]
  action_id?: string
  status?: string
  expires_at?: string
}

export interface StreamAccumulator {
  runId: string
  createdAt: string
  parts: StreamingPart[]
  inputText: Record<string, string>
}

export function createAccumulator(runId: string): StreamAccumulator {
  return { runId, createdAt: new Date().toISOString(), parts: [], inputText: {} }
}

function appendText(accumulator: StreamAccumulator, type: 'text' | 'reasoning', value: string) {
  if (!value) return
  const last = accumulator.parts.at(-1)
  if (last?.type === type) {
    if (type === 'text') last.text = (last.text ?? '') + value
    else last.reasoning = (last.reasoning ?? '') + value
    return
  }
  accumulator.parts.push({
    id: `stream-${accumulator.parts.length}`,
    order_index: accumulator.parts.length,
    type,
    ...(type === 'text' ? { text: value } : { reasoning: value }),
  })
}

function toolPart(accumulator: StreamAccumulator, callId: string): StreamingPart | undefined {
  return accumulator.parts.find(
    (part) => part.type === 'tool_call' && part.tool_call_id === callId,
  )
}

function parsedInput(value: string): unknown {
  if (!value) return undefined
  try {
    return JSON.parse(value)
  } catch {
    return value
  }
}

export function applyStreamEvent(
  accumulator: StreamAccumulator,
  event: AgentStreamEvent,
): void {
  switch (event.type) {
    case 'text_delta':
      appendText(accumulator, 'text', event.text ?? '')
      break
    case 'reasoning_delta':
      appendText(accumulator, 'reasoning', event.text ?? '')
      break
    case 'tool_input_start': {
      const callId = event.tool_call_id ?? `call-${accumulator.parts.length}`
      if (toolPart(accumulator, callId)) break
      accumulator.parts.push({
        id: `stream-${accumulator.parts.length}`,
        order_index: accumulator.parts.length,
        type: 'tool_call',
        tool_call_id: callId,
        tool_name: event.tool_name,
        state: 'running',
      })
      break
    }
    case 'tool_input_delta': {
      const callId = event.tool_call_id ?? ''
      accumulator.inputText[callId] = (accumulator.inputText[callId] ?? '') + (event.text ?? '')
      const part = toolPart(accumulator, callId)
      if (part) part.tool_input = parsedInput(accumulator.inputText[callId])
      break
    }
    case 'tool_output': {
      const callId = event.tool_call_id ?? ''
      let part = toolPart(accumulator, callId)
      if (!part) {
        part = {
          id: `stream-${accumulator.parts.length}`,
          order_index: accumulator.parts.length,
          type: 'tool_call',
          tool_call_id: callId,
          tool_name: event.tool_name,
        }
        accumulator.parts.push(part)
      }
      part.tool_input = event.tool_input ?? parsedInput(accumulator.inputText[callId] ?? '')
      part.tool_output = event.tool_output
      part.loading_message = event.loading_message
      part.state = event.is_error ? 'error' : 'done'
      if (event.is_error) part.error = 'Tool call failed'
      break
    }
  }
}

export function snapshotAccumulator(accumulator: StreamAccumulator): StreamingMessage {
  return {
    id: `stream-${accumulator.runId}`,
    runId: accumulator.runId,
    createdAt: accumulator.createdAt,
    parts: accumulator.parts.map((part) => ({ ...part })),
  }
}
