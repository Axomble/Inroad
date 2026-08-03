import { skipToken } from '@reduxjs/toolkit/query'
import { useAppSelector } from '@/store/hooks'
import { useGetPulseQuery } from '@/features/pulse/api'

/**
 * The chrome's one subscription to the workspace pulse read-model.
 *
 * Layout components may reuse a feature's *query hook* (the `useNavCounts`
 * doctrine) but never feature UI; this hook is that seam for the pulse
 * endpoint, and it pins the polling options in exactly one place so every
 * consumer (pulse card, nav counts, the header's attention dot) shares a
 * single deduped RTK Query subscription instead of each choosing its own
 * cadence.
 *
 * Skipped until a workspace is active — the server resolves the workspace
 * from the session, so before it settles there is nothing truthful to ask for.
 */
export function usePulse() {
  const workspaceId = useAppSelector((s) => s.auth.activeWorkspaceId)
  return useGetPulseQuery(workspaceId ? undefined : skipToken, {
    pollingInterval: 45_000,
    skipPollingIfUnfocused: true,
  })
}
