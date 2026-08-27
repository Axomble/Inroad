import { useCallback, useEffect, useReducer, useRef } from 'react'
import { useStore } from 'react-redux'
import type { RootState } from '@/store'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import { applyEnvelopeToCache } from './cache-patch'
import { RealtimeClient } from './socket-client'
import {
  initialRealtimeState,
  isSelfEcho,
  realtimeReducer,
  type RealtimeEnvelope,
  type RealtimeState,
} from './socket-events'

/**
 * Owns the single workspace socket. Mirrors `use-agent-stream.ts`: the React
 * layer holds the effect and the store handles, and every rule worth testing
 * lives in the two modules it calls.
 *
 * Deliberately NOT in Redux. Connection status is per-tab, changes on a network
 * blip, and must never be persisted — `store/index.ts` whitelists `['ui']` only
 * and `store/__tests__/persist-whitelist.test.ts` enforces it. A `useReducer`
 * here keeps the state where its only consumer is (the indicator, via the
 * provider's context) with no way to leak into storage.
 */
export function useRealtime(): RealtimeState {
  const dispatch = useAppDispatch()
  const store = useStore<RootState>()
  const token = useAppSelector((state) => state.auth.accessToken)
  const workspaceId = useAppSelector((state) => state.auth.activeWorkspaceId)
  const userId = useAppSelector((state) => state.auth.userId)

  const [state, act] = useReducer(realtimeReducer, initialRealtimeState)

  // Read through refs inside the socket callbacks: the client outlives any one
  // render, and closing over the rendered values would pin it to the first one.
  const lastSeqRef = useRef(0)
  const userIdRef = useRef(userId)
  userIdRef.current = userId
  lastSeqRef.current = state.lastSeq

  const handleEnvelope = useCallback(
    (envelope: RealtimeEnvelope) => {
      // Order matters: the seq must advance even for an echo we drop, or the
      // next reconnect asks the server to replay a frame we deliberately
      // ignored and the gap check trips on our own decision.
      act({ kind: 'frame', envelope })
      if (isSelfEcho(envelope, userIdRef.current)) return
      applyEnvelopeToCache(envelope, dispatch, store.getState())
    },
    [dispatch, store],
  )

  useEffect(() => {
    if (!token || !workspaceId) {
      act({ kind: 'reset' })
      return
    }
    act({ kind: 'connecting' })
    const client = new RealtimeClient({
      token,
      lastSeq: () => lastSeqRef.current,
      onOpen: () => act({ kind: 'open' }),
      onEnvelope: handleEnvelope,
      onClose: () => act({ kind: 'closed' }),
      onFatal: (message) => act({ kind: 'failed', error: message }),
    })
    client.start()
    // The workspace is pinned in the ticket (spec §7.1), so switching workspace
    // is a new socket, not a resubscribe. Token loss (useAuthGuard's trigger) is
    // the disconnect.
    return () => client.stop()
  }, [token, workspaceId, handleEnvelope])

  return state
}
