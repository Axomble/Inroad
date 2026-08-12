import { useAppSelector } from '@/store/hooks'
import { hasMinRole, type WorkspaceRole } from '@/lib/rbac'

/**
 * Whether the active workspace membership meets `minRole`.
 *
 * The React seam over `hasMinRole` — the rule itself stays pure and unit-tested
 * in `@/lib/rbac`; this only supplies the role from the auth slice.
 *
 * It lives in `hooks/` rather than `features/auth/` because five different
 * places need it (the settings rail plus the team, API-key, connected-apps and
 * AI panels), and four of those are outside the auth feature — importing it
 * from there would be a cross-feature import that the "read-only RTK Query
 * hooks" exception doesn't cover. Workspace role is cross-cutting, so it gets
 * a neutral home like `rtk-error` and `theme` did.
 */
export function useHasRole(minRole: WorkspaceRole): boolean {
  const role = useAppSelector((state) => state.auth.role)
  return hasMinRole(role, minRole)
}
