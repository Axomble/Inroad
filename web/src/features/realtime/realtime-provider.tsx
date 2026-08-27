import type { ReactNode } from 'react'
import { RealtimeContext } from './realtime-context'
import { useRealtime } from './use-realtime'

/**
 * Mounts the workspace socket exactly once and publishes its status.
 *
 * Belongs in `app.tsx`'s `AppLayout` — not `main.tsx` (which mounts before auth
 * bootstrap resolves) and not `__root.tsx` (which covers login, register and
 * OAuth consent, where a socket would connect with no session). `AppLayout` is
 * the first point an access token is guaranteed, and its `useAuthGuard` is the
 * disconnect trigger.
 */
export function RealtimeProvider({ children }: { children: ReactNode }) {
  const state = useRealtime()
  return <RealtimeContext.Provider value={state}>{children}</RealtimeContext.Provider>
}
