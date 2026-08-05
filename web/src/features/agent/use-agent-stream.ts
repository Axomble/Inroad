import { useEffect, useRef } from 'react'
import { useStore } from 'react-redux'
import { api } from '@/store/api'
import type { RootState } from '@/store'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import {
  clearSubmittedAgentDraft,
  replaceAgentMessages,
  setAgentQueue,
  setAgentStreamError,
  setAgentStreamStatus,
  setLastAgentEventId,
  setStreamingMessage,
} from '@/store/slices/agent'
import { agentApi } from './api'
import {
  applyStreamEvent,
  createAccumulator,
  snapshotAccumulator,
  type AgentStreamEvent,
  type StreamAccumulator,
} from './stream-state'
import { AgentStreamHttpError, openAgentStream, readSSE, type SSEFrame } from './stream-client'

const touchedTags = {
  campaign: 'Campaign',
  contact: 'Contact',
  list: 'List',
  mailbox: 'Mailbox',
} as const

const deltaEvents = new Set<AgentStreamEvent['type']>([
  'text_delta',
  'reasoning_delta',
  'tool_input_start',
  'tool_input_delta',
  'tool_output',
])

/** Give up after this many consecutive failed reconnects rather than looping silently forever. */
const maxReconnectAttempts = 8

function retryDelay(attempt: number): number {
  return Math.min(5000, 400 * 2 ** Math.min(attempt, 4))
}

/**
 * True when a failed connect is worth retrying. A 4xx is the server telling us
 * this request will never succeed (thread deleted, not ours, malformed); 408 is
 * the one exception that explicitly means "try again". Everything else —
 * network drops, 5xx, an aborted body — is transient.
 *
 * 429 is deliberately terminal here even though it is a "try again" status
 * elsewhere: on this endpoint it means the per-user cap on concurrent streams
 * is full, which no amount of waiting fixes if the other streams are the user's
 * own open tabs. Reconnecting every five seconds against a cap is the silent
 * spin this whole classification exists to prevent.
 */
function isRetryable(error: unknown): boolean {
  if (!(error instanceof AgentStreamHttpError)) return true
  if (error.status === 408) return true
  return error.status < 400 || error.status >= 500
}

function terminalMessage(error: unknown): string {
  if (!(error instanceof AgentStreamHttpError)) {
    return 'Lost the connection to the assistant. Reopen this conversation to reconnect.'
  }
  switch (error.status) {
    case 401:
      return 'Your session expired. Refresh the page and try again.'
    case 403:
      return 'You no longer have access to this conversation.'
    case 404:
      return 'This conversation no longer exists. Start a new one.'
    case 429:
      return 'Too many assistant conversations are open at once. Close one in another tab, then reopen this conversation.'
    default:
      return `The assistant stream was refused (HTTP ${error.status}). Start a new conversation.`
  }
}

function wait(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = window.setTimeout(resolve, ms)
    signal.addEventListener(
      'abort',
      () => {
        window.clearTimeout(timer)
        resolve()
      },
      { once: true },
    )
  })
}

/**
 * Owns the single SSE subscription for the active thread.
 *
 * Two invariants keep the panel honest:
 *
 * 1. The persisted thread is the source of truth for what has been said and
 *    whether a run is active; the stream only carries the in-flight delta. So
 *    every terminal event *and* every successful (re)connect refetches the
 *    thread — the server drops the event log when a run ends, so a client that
 *    reconnects afterwards would otherwise replay an empty backlog and sit on a
 *    stale partial bubble forever.
 * 2. The accumulator is cleared whenever the transcript takes over, so a late
 *    `done` (title generation lands between `message_persisted` and `done`)
 *    cannot re-flush a snapshot of a message that is already rendered from the
 *    transcript.
 */
export function useAgentStream(): void {
  const dispatch = useAppDispatch()
  const store = useStore<RootState>()
  const threadId = useAppSelector((state) => state.agent.activeThreadId)
  const token = useAppSelector((state) => state.auth.accessToken)

  const accumulatorRef = useRef<StreamAccumulator | null>(null)
  const timerRef = useRef<number | null>(null)
  const afterRef = useRef(0)
  const trackedThreadRef = useRef<string | null>(null)

  if (trackedThreadRef.current !== threadId) {
    trackedThreadRef.current = threadId
    // Read once instead of subscribing: the seq changes on every frame, and a
    // selector on it would re-render the whole panel thousands of times per
    // answer. `afterRef` is what the reconnect actually uses.
    afterRef.current = threadId ? (store.getState().agent.lastEventIds[threadId] ?? 0) : 0
    accumulatorRef.current = null
  }

  useEffect(() => {
    if (!threadId || !token) return
    const controller = new AbortController()
    const { signal } = controller

    /** Persist the resume cursor. Called on flush/terminal events, never per frame. */
    const commitCursor = () => {
      if (afterRef.current > 0) {
        dispatch(setLastAgentEventId({ threadId, eventId: afterRef.current }))
      }
    }

    const flush = () => {
      timerRef.current = null
      if (!accumulatorRef.current) return
      dispatch(setStreamingMessage(snapshotAccumulator(accumulatorRef.current)))
      commitCursor()
    }

    const scheduleFlush = () => {
      if (timerRef.current === null) timerRef.current = window.setTimeout(flush, 100)
    }

    const cancelFlush = () => {
      if (timerRef.current !== null) {
        window.clearTimeout(timerRef.current)
        timerRef.current = null
      }
    }

    /** Hand the screen back to the persisted transcript and retire the in-flight snapshot. */
    const refreshTranscript = async () => {
      try {
        const thread = await dispatch(
          agentApi.endpoints.getAgentThread.initiate(
            { id: threadId },
            { forceRefetch: true, subscribe: false },
          ),
        ).unwrap()
        if (signal.aborted) return
        cancelFlush()
        accumulatorRef.current = null
        dispatch(replaceAgentMessages(thread.messages ?? []))
        dispatch(setStreamingMessage(null))
        dispatch(clearSubmittedAgentDraft())
        dispatch(setAgentStreamStatus(thread.active_run_id ? 'running' : 'idle'))
        dispatch(agentApi.util.invalidateTags(['AgentThreadList']))
      } catch {
        // The stream remains authoritative until the next event/reconnect.
      }
    }

    const handleEvent = (event: AgentStreamEvent) => {
      const runId = event.run_id ?? accumulatorRef.current?.runId ?? 'unknown'
      if (deltaEvents.has(event.type)) {
        if (!accumulatorRef.current || accumulatorRef.current.runId !== runId) {
          accumulatorRef.current = createAccumulator(runId)
        }
        applyStreamEvent(accumulatorRef.current, event)
        scheduleFlush()
      }

      switch (event.type) {
        case 'queue_updated':
          dispatch(setAgentQueue(event.queued ?? []))
          break
        case 'message_persisted':
        case 'approval_required':
          dispatch(agentApi.util.invalidateTags(['AgentApproval']))
          commitCursor()
          void refreshTranscript()
          break
        case 'thread_title':
          dispatch(agentApi.util.invalidateTags(['AgentThread', 'AgentThreadList']))
          break
        case 'run_error':
          flush()
          accumulatorRef.current = null
          dispatch(setAgentStreamError(event.text || 'The assistant could not complete this run.'))
          break
        case 'done': {
          flush()
          cancelFlush()
          accumulatorRef.current = null
          dispatch(setAgentStreamStatus('idle'))
          dispatch(clearSubmittedAgentDraft())
          commitCursor()
          const tags = (event.object_types ?? [])
            .map((objectType) => touchedTags[objectType as keyof typeof touchedTags])
            .filter((tag): tag is (typeof touchedTags)[keyof typeof touchedTags] => Boolean(tag))
          if (tags.length > 0) dispatch(api.util.invalidateTags(tags))
          break
        }
      }
    }

    let receivedFrame = false
    const handleFrame = (frame: SSEFrame) => {
      receivedFrame = true
      afterRef.current = frame.id
      handleEvent(frame.event)
    }

    const connect = async () => {
      let attempt = 0
      dispatch(setAgentStreamStatus('connecting'))
      while (!signal.aborted) {
        try {
          receivedFrame = false
          // oxlint-disable-next-line no-await-in-loop -- reconnect attempts are deliberately serialized
          const response = await openAgentStream(threadId, token, afterRef.current, signal)
          dispatch(setAgentStreamStatus('idle'))
          // The backlog we just resumed from may be empty because the run
          // finished and the server dropped the log. The thread knows.
          // oxlint-disable-next-line no-await-in-loop -- the transcript must settle before frames apply on top of it
          await refreshTranscript()
          // oxlint-disable-next-line no-await-in-loop -- only one reader may own the response stream
          await readSSE(response, handleFrame, signal)
        } catch (error) {
          if (signal.aborted) return
          if (!isRetryable(error)) {
            dispatch(setAgentStreamError(terminalMessage(error)))
            return
          }
          if (attempt >= maxReconnectAttempts) {
            dispatch(setAgentStreamError(terminalMessage(error)))
            return
          }
        }
        if (!signal.aborted) {
          // Progress resets the budget; a connection that opens and closes
          // without ever delivering a frame counts against it, so a flapping
          // server can't keep the panel spinning indefinitely.
          if (receivedFrame) attempt = 0
          else if (attempt >= maxReconnectAttempts) {
            dispatch(
              setAgentStreamError(
                'Lost the connection to the assistant. Reopen this conversation to reconnect.',
              ),
            )
            return
          }
          dispatch(setAgentStreamStatus('connecting'))
          // oxlint-disable-next-line no-await-in-loop -- backoff must finish before the next reconnect attempt
          await wait(retryDelay(attempt++), signal)
        }
      }
    }

    void connect()
    return () => {
      controller.abort()
      cancelFlush()
    }
  }, [dispatch, store, threadId, token])
}
