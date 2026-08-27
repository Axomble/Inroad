import { createContext, useContext } from 'react'
import { initialRealtimeState, type RealtimeState } from './socket-events'

/**
 * Connection status, published to whoever renders the indicator.
 *
 * A separate module from `realtime-provider.tsx` only so that file exports a
 * component and nothing else — Vite's fast refresh gives up on a module that
 * mixes components with other exports, and oxlint's `only-export-components`
 * flags it.
 *
 * The context carries status ONLY. Events go straight into the RTK Query cache,
 * so nothing subscribes to an event stream through here and a socket frame
 * re-renders exactly the components whose cached data changed.
 */
export const RealtimeContext = createContext<RealtimeState>(initialRealtimeState)

export function useRealtimeStatus(): RealtimeState {
  return useContext(RealtimeContext)
}
