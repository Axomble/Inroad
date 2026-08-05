import { useEffect, useRef } from 'react'
import { api } from '@/store/api'
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
import { openAgentStream, readSSE, type SSEFrame } from './stream-client'

const touchedTags = {
  campaign: 'Campaign',
  contact: 'Contact',
  list: 'List',
  mailbox: 'Mailbox',
} as const

function retryDelay(attempt: number): number {
  return Math.min(5000, 400 * 2 ** Math.min(attempt, 4))
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

export function useAgentStream(): void {
  const dispatch = useAppDispatch()
  const threadId = useAppSelector((state) => state.agent.activeThreadId)
  const token = useAppSelector((state) => state.auth.accessToken)
  const storedEventId = useAppSelector((state) =>
    threadId ? (state.agent.lastEventIds[threadId] ?? 0) : 0,
  )

  const accumulatorRef = useRef<StreamAccumulator | null>(null)
  const timerRef = useRef<number | null>(null)
  const afterRef = useRef(0)
  const trackedThreadRef = useRef<string | null>(null)

  if (trackedThreadRef.current !== threadId) {
    trackedThreadRef.current = threadId
    afterRef.current = storedEventId
    accumulatorRef.current = null
  }

  useEffect(() => {
    if (!threadId || !token) return
    const controller = new AbortController()
    const { signal } = controller

    const flush = () => {
      timerRef.current = null
      if (accumulatorRef.current) {
        dispatch(setStreamingMessage(snapshotAccumulator(accumulatorRef.current)))
      }
    }

    const scheduleFlush = () => {
      if (timerRef.current === null) timerRef.current = window.setTimeout(flush, 100)
    }

    const refreshTranscript = async () => {
      try {
        const thread = await dispatch(
          agentApi.endpoints.getAgentThread.initiate(
            { id: threadId },
            { forceRefetch: true, subscribe: false },
          ),
        ).unwrap()
        if (signal.aborted) return
        dispatch(replaceAgentMessages(thread.messages ?? []))
        dispatch(setStreamingMessage(null))
        dispatch(clearSubmittedAgentDraft())
        dispatch(agentApi.util.invalidateTags(['AgentThreadList']))
      } catch {
        // The stream remains authoritative until the next event/reconnect.
      }
    }

    const handleEvent = (event: AgentStreamEvent) => {
      const runId = event.run_id ?? accumulatorRef.current?.runId ?? 'unknown'
      if (
        ['text_delta', 'reasoning_delta', 'tool_input_start', 'tool_input_delta', 'tool_output'].includes(
          event.type,
        )
      ) {
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
          dispatch(agentApi.util.invalidateTags(['AgentApproval']))
          void refreshTranscript()
          break
        case 'approval_required':
          dispatch(agentApi.util.invalidateTags(['AgentApproval']))
          void refreshTranscript()
          break
        case 'thread_title':
          dispatch(agentApi.util.invalidateTags(['AgentThread', 'AgentThreadList']))
          break
        case 'run_error':
          flush()
          dispatch(setAgentStreamError(event.text || 'The assistant could not complete this run.'))
          break
        case 'done': {
          flush()
          dispatch(setAgentStreamStatus('idle'))
          dispatch(clearSubmittedAgentDraft())
          const tags = (event.object_types ?? [])
            .map((objectType) => touchedTags[objectType as keyof typeof touchedTags])
            .filter((tag): tag is (typeof touchedTags)[keyof typeof touchedTags] => Boolean(tag))
          if (tags.length > 0) dispatch(api.util.invalidateTags(tags))
          break
        }
      }
    }

    const handleFrame = (frame: SSEFrame) => {
      afterRef.current = frame.id
      dispatch(setLastAgentEventId({ threadId, eventId: frame.id }))
      handleEvent(frame.event)
    }

    const connect = async () => {
      let attempt = 0
      dispatch(setAgentStreamStatus('connecting'))
      while (!signal.aborted) {
        try {
          // oxlint-disable-next-line no-await-in-loop -- reconnect attempts are deliberately serialized
          const response = await openAgentStream(threadId, token, afterRef.current, signal)
          attempt = 0
          dispatch(setAgentStreamStatus('idle'))
          // oxlint-disable-next-line no-await-in-loop -- only one reader may own the response stream
          await readSSE(response, handleFrame, signal)
        } catch (error) {
          if (signal.aborted) return
          if (error instanceof Error && error.message.includes('HTTP 401')) {
            dispatch(setAgentStreamError('Your session expired. Refresh the page and try again.'))
            return
          }
        }
        if (!signal.aborted) {
          dispatch(setAgentStreamStatus('connecting'))
          // oxlint-disable-next-line no-await-in-loop -- backoff must finish before the next reconnect attempt
          await wait(retryDelay(attempt++), signal)
        }
      }
    }

    void connect()
    return () => {
      controller.abort()
      if (timerRef.current !== null) {
        window.clearTimeout(timerRef.current)
        timerRef.current = null
      }
    }
  }, [dispatch, threadId, token])
}
