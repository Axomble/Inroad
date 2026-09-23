// Branch writes the graph on screen can't include yet. Pure.
//
// Why an overlay and not a cache patch: every branch write invalidates the
// graph, so by the time a PUT resolves its refetch is already in flight — and
// while a refetch is in flight RTK Query's hook returns the PREVIOUS result's
// data, so a patched cache entry isn't what the canvas renders. A second exit
// drag made in that window would be built from the stale branch and silently
// revert the first. The overlay is what the canvas renders, and builds from,
// until a graph response that must contain the write arrives.
//
// "Must contain" is decided by time: a write is superseded once the graph on
// screen came from a request that STARTED after the write finished (the server
// commits a write before answering it, so any read begun later sees it). While
// a refetch is in flight the data shown is older than that request, so every
// write stays applied.
//
// Across tabs this is still last-writer-wins: another tab's write lands only
// when this tab's graph refetches (on focus, and before the condition editor
// opens), and a save here replaces whatever the server had. Making that a
// conflict instead needs a server-side precondition (expected_updated_at) —
// tracked as a backend follow-up.
import type { StepBranch } from './api'

/** One completed write: the step's branch as the server answered (null = removed). */
export type BranchWrite = { stepId: string; branch: StepBranch | null; savedAt: number }

/** Where the graph on screen came from. `startedAt` is that request's start. */
export type GraphFreshness = { isFetching: boolean; startedAt: number | undefined }

export function isSuperseded(write: BranchWrite, freshness: GraphFreshness): boolean {
  return !freshness.isFetching && freshness.startedAt !== undefined && freshness.startedAt > write.savedAt
}

/** The server's branches with every not-yet-superseded write applied, oldest first. */
export function applyBranchWrites(
  server: ReadonlyMap<string, StepBranch>,
  writes: readonly BranchWrite[],
  freshness: GraphFreshness,
): Map<string, StepBranch> {
  const branches = new Map(server)
  for (const write of writes) {
    if (isSuperseded(write, freshness)) continue
    if (write.branch) branches.set(write.stepId, write.branch)
    else branches.delete(write.stepId)
  }
  return branches
}
