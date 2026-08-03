import { skipToken } from '@reduxjs/toolkit/query'
import { useAppSelector } from '@/store/hooks'
import { useGetPulseQuery, type WorkspacePulse } from '@/features/pulse/api'

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
const POLL_OPTIONS = { pollingInterval: 45_000, skipPollingIfUnfocused: true } as const

function usePulseArg() {
  const workspaceId = useAppSelector((s) => s.auth.activeWorkspaceId)
  return workspaceId ? undefined : skipToken
}

/** The full query result — for the pulse card, which renders freshness/error state. */
export function usePulse() {
  return useGetPulseQuery(usePulseArg(), POLL_OPTIONS)
}

/**
 * A narrowed subscription for consumers that only need a slice of the payload
 * (nav counts, the header's danger flag). `selectFromResult` means they
 * re-render only when their selected values change — not on every poll tick's
 * timestamp churn, which would otherwise re-render the whole sidebar (both
 * mounts of it) every 45s for identical data.
 */
export function usePulseSelect<T extends Record<string, unknown>>(
  select: (data: WorkspacePulse | undefined) => T,
): T {
  return useGetPulseQuery(usePulseArg(), {
    ...POLL_OPTIONS,
    selectFromResult: ({ data }) => select(data),
  })
}
