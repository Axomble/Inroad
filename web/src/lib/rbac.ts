/**
 * The workspace role model, mirroring the server's.
 *
 * The backend does not ask "is this caller an admin" anywhere — it ranks roles
 * and admits a request when the caller's rank meets the route's declared
 * minimum (`auth.RequireRole("admin")` over `roleRank` in
 * `internal/app/auth/middleware.go`). This module is that same primitive for
 * the client, so a UI affordance and the route behind it are gated by one
 * shared rule rather than by an `=== 'owner' || === 'admin'` comparison
 * re-expanded at each call site — which silently mis-ranks the moment a role
 * is added between two existing ones.
 *
 * This is a *rendering* decision, never an authorization one. The server
 * enforces the real boundary on every request; hiding a control the server
 * would 403 keeps the UI honest, and every gated screen still renders its own
 * insufficient-role state when reached by deep link.
 */

/** Roles a workspace membership can hold, lowest authority first. */
export const WORKSPACE_ROLES = ['member', 'admin', 'owner'] as const

export type WorkspaceRole = (typeof WORKSPACE_ROLES)[number]

/**
 * Rank per role. Derived from the ordering above rather than written out
 * twice, so the two can't disagree. Mirrors `roleRank` on the server, which
 * ranks from 1 — the values only ever get compared to each other, but keeping
 * the offset identical makes the two tables diffable by eye.
 */
const ROLE_RANK: Record<WorkspaceRole, number> = Object.fromEntries(
  WORKSPACE_ROLES.map((role, index) => [role, index + 1]),
) as Record<WorkspaceRole, number>

/**
 * Rank of an arbitrary role string. Anything the client doesn't recognize —
 * `null` before the session settles, or a role from a newer server — ranks 0
 * and therefore satisfies no minimum. Fail-closed, exactly as the server's
 * `roleRank[p.Role] < want` treats an unknown role.
 */
function rankOf(role: string | null | undefined): number {
  if (role == null) return 0
  return ROLE_RANK[role as WorkspaceRole] ?? 0
}

/**
 * Whether `role` meets `minRole` — the client-side reading of
 * `auth.RequireRole(minRole)`.
 */
export function hasMinRole(role: string | null | undefined, minRole: WorkspaceRole): boolean {
  return rankOf(role) >= ROLE_RANK[minRole]
}
