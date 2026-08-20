import { act, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { selectAgentThread, setStreamingMessage } from '@/store/slices/agent'
import type { AgentMessage } from '../api'
import { AgentStreamHttpError, type SSEFrame } from '../stream-client'
import type { AgentStreamEvent } from '../stream-state'
import { useAgentStream } from '../use-agent-stream'

const mocks = vi.hoisted(() => ({
  open: vi.fn(),
  read: vi.fn(),
}))

vi.mock('../stream-client', async (importOriginal) => {
  const original = await importOriginal<typeof import('../stream-client')>()
  return { ...original, openAgentStream: mocks.open, readSSE: mocks.read }
})

const jsonHeaders = { 'content-type': 'application/json' }
const THREAD_ID = 'thread-1'

function assistantMessage(text: string): AgentMessage {
  return {
    id: 'message-1',
    turn_id: 'turn-1',
    role: 'assistant',
    status: 'sent',
    created_at: '2026-08-05T10:00:00Z',
    parts: [{ id: 'part-1', order_index: 0, type: 'text', text }],
  }
}

/** What GET /agent/threads/:id answers with; tests mutate it between events. */
let thread: { id: string; title: string; active_run_id: string | null; messages: AgentMessage[] }

/** Resolves the in-flight readSSE, i.e. the server closed the stream. */
let endRead: (() => void) | null = null
let emit: ((frame: SSEFrame) => void) | null = null
let seq = 0

function Probe() {
  useAgentStream()
  return null
}

function send(event: AgentStreamEvent) {
  seq += 1
  emit?.({ id: seq, event })
}

beforeEach(() => {
  seq = 0
  emit = null
  endRead = null
  thread = { id: THREAD_ID, title: 'Campaign check', active_run_id: 'run-1', messages: [] }

  mocks.open.mockReset()
  mocks.read.mockReset()
  mocks.open.mockResolvedValue(new Response('', { status: 200 }))
  mocks.read.mockImplementation(async (_response: Response, onFrame: (frame: SSEFrame) => void) => {
    emit = onFrame
    await new Promise<void>((resolve) => {
      endRead = resolve
    })
  })

  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify(thread), { status: 200, headers: jsonHeaders })),
  )
})

afterEach(() => {
  endRead?.()
  vi.unstubAllGlobals()
})

function renderStream() {
  const rendered = renderWithProviders(<Probe />, {
    preloadedState: { auth: { accessToken: 'token-1' } },
  })
  act(() => {
    rendered.store.dispatch(selectAgentThread(THREAD_ID))
  })
  return rendered
}

describe('useAgentStream', () => {
  it('does not resurrect the streaming bubble when done arrives after message_persisted', async () => {
    const { store } = renderStream()
    await waitFor(() => expect(mocks.read).toHaveBeenCalled())

    act(() => {
      send({ type: 'text_delta', run_id: 'run-1', text: 'All good.' })
    })
    await waitFor(() => expect(store.getState().agent.streaming).not.toBeNull())

    // The server persists the message, then spends seconds generating a title
    // before emitting `done` — the window where the stale accumulator used to
    // be re-flushed on top of the transcript.
    thread = { ...thread, active_run_id: null, messages: [assistantMessage('All good.')] }
    act(() => {
      send({ type: 'message_persisted', run_id: 'run-1' })
    })
    await waitFor(() => expect(store.getState().agent.messageIds).toEqual(['message-1']))

    act(() => {
      send({ type: 'thread_title', title: 'All good' })
      send({ type: 'done', run_id: 'run-1' })
    })

    // Give the 100 ms throttle more than enough time to fire a stale flush.
    await new Promise((resolve) => setTimeout(resolve, 250))
    expect(store.getState().agent.streaming).toBeNull()
    expect(store.getState().agent.messageIds).toEqual(['message-1'])
    expect(store.getState().agent.streamStatus).toBe('idle')
  })

  it('recovers from a reconnect whose backlog is empty because the run already finished', async () => {
    const { store } = renderStream()
    await waitFor(() => expect(mocks.read).toHaveBeenCalledTimes(1))

    // A partial answer is on screen when the connection drops.
    act(() => {
      store.dispatch(
        setStreamingMessage({
          id: 'stream-run-1',
          runId: 'run-1',
          createdAt: '2026-08-05T10:00:00Z',
          parts: [{ id: 'p', order_index: 0, type: 'text', text: 'All go' }],
        }),
      )
    })
    expect(store.getState().agent.streaming).not.toBeNull()

    // The run finished while disconnected: the server dropped the event log,
    // so the replayed backlog is empty and no `done` will ever arrive.
    thread = { ...thread, active_run_id: null, messages: [assistantMessage('All good.')] }
    act(() => {
      endRead?.()
      endRead = null
    })

    await waitFor(() => expect(mocks.open).toHaveBeenCalledTimes(2), { timeout: 3000 })
    await waitFor(() => {
      expect(store.getState().agent.streaming).toBeNull()
      expect(store.getState().agent.messageIds).toEqual(['message-1'])
      expect(store.getState().agent.streamStatus).toBe('idle')
    })
  })

  it('stops and reports a 403 instead of reconnecting forever', async () => {
    mocks.open.mockRejectedValue(new AgentStreamHttpError(403))
    const { store } = renderStream()

    await waitFor(() =>
      expect(store.getState().agent.streamError).toBe('You no longer have access to this conversation.'),
    )
    const attempts = mocks.open.mock.calls.length
    await new Promise((resolve) => setTimeout(resolve, 600))
    expect(mocks.open).toHaveBeenCalledTimes(attempts)
  })

  it('treats the concurrent-stream cap (429) as terminal with actionable copy', async () => {
    mocks.open.mockRejectedValue(new AgentStreamHttpError(429))
    const { store } = renderStream()

    await waitFor(() =>
      expect(store.getState().agent.streamError).toBe(
        'Too many assistant conversations are open at once. Close one in another tab, then reopen this conversation.',
      ),
    )
    const attempts = mocks.open.mock.calls.length
    await new Promise((resolve) => setTimeout(resolve, 600))
    expect(mocks.open).toHaveBeenCalledTimes(attempts)
  })

  it('resumes from the last id it saw, whatever the thread counter is at', async () => {
    const { store } = renderStream()
    await waitFor(() => expect(mocks.read).toHaveBeenCalledTimes(1))
    expect(mocks.open).toHaveBeenLastCalledWith(THREAD_ID, 'token-1', 0, expect.anything())

    // Ids are monotonic per thread and outlive individual runs, so the client
    // must echo the last id back verbatim rather than assume a per-run origin.
    seq = 5100
    act(() => {
      send({ type: 'text_delta', run_id: 'run-2', text: 'hi' })
    })
    await waitFor(() => expect(store.getState().agent.streaming).not.toBeNull())

    act(() => {
      endRead?.()
      endRead = null
    })
    await waitFor(
      () => expect(mocks.open).toHaveBeenLastCalledWith(THREAD_ID, 'token-1', 5101, expect.anything()),
      { timeout: 3000 },
    )
  })

  it('keeps retrying a 500 without surfacing an error', async () => {
    mocks.open.mockRejectedValue(new AgentStreamHttpError(500))
    const { store } = renderStream()

    await waitFor(() => expect(mocks.open.mock.calls.length).toBeGreaterThan(1), { timeout: 3000 })
    expect(store.getState().agent.streamError).toBeNull()
  })
})
