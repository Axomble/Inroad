import { config } from '@/lib/config'
import type { AgentStreamEvent } from './stream-state'

export interface SSEFrame {
  id: number
  event: AgentStreamEvent
}

/**
 * A stream request that reached the server and was refused. Carries the status
 * so the caller can decide between "retry" and "give up and tell the user"
 * without string-matching the message — the SSE stream is opened with `fetch`,
 * so it never flows through the RTK error seam in `@/lib/rtk-error`.
 */
export class AgentStreamHttpError extends Error {
  readonly status: number

  constructor(status: number) {
    super(`Agent stream returned HTTP ${status}.`)
    this.name = 'AgentStreamHttpError'
    this.status = status
  }
}

function apiURL(path: string): string {
  const base = config.apiBaseUrl
  const normalized = base.endsWith('/') ? base.slice(0, -1) : base
  return new URL(`${normalized}${path}`, window.location.origin).toString()
}

function parseFrame(raw: string): SSEFrame | null {
  let id = 0
  const data: string[] = []
  for (const line of raw.split('\n')) {
    if (line.startsWith('id:')) id = Number(line.slice(3).trim())
    if (line.startsWith('data:')) data.push(line.slice(5).trimStart())
  }
  if (!Number.isSafeInteger(id) || id < 1 || data.length === 0) return null
  try {
    return { id, event: JSON.parse(data.join('\n')) as AgentStreamEvent }
  } catch {
    return null
  }
}

export async function readSSE(
  response: Response,
  onFrame: (frame: SSEFrame) => void,
  signal: AbortSignal,
): Promise<void> {
  if (!response.body) throw new Error('The browser did not provide a response stream.')
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  try {
    while (!signal.aborted) {
      // oxlint-disable-next-line no-await-in-loop -- stream chunks must be read and parsed in wire order
      const { value, done } = await reader.read()
      if (done) {
        buffer += decoder.decode()
        break
      }
      buffer += decoder.decode(value, { stream: true })
      buffer = buffer.replaceAll('\r\n', '\n')
      let boundary = buffer.indexOf('\n\n')
      while (boundary >= 0) {
        const frame = parseFrame(buffer.slice(0, boundary))
        buffer = buffer.slice(boundary + 2)
        if (frame) onFrame(frame)
        boundary = buffer.indexOf('\n\n')
      }
    }
    buffer = buffer.replaceAll('\r\n', '\n').trim()
    if (buffer) {
      const frame = parseFrame(buffer)
      if (frame) onFrame(frame)
    }
  } finally {
    reader.releaseLock()
  }
}

export async function openAgentStream(
  threadId: string,
  token: string,
  after: number,
  signal: AbortSignal,
): Promise<Response> {
  const url = new URL(apiURL(`/agent/threads/${threadId}/stream`))
  if (after > 0) url.searchParams.set('after_seq', String(after))
  const response = await fetch(url, {
    headers: {
      Accept: 'text/event-stream',
      Authorization: `Bearer ${token}`,
      ...(after > 0 ? { 'Last-Event-ID': String(after) } : {}),
    },
    credentials: 'include',
    signal,
  })
  if (!response.ok) throw new AgentStreamHttpError(response.status)
  return response
}
