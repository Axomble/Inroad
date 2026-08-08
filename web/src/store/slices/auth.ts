import { createSlice, type PayloadAction } from '@reduxjs/toolkit'

/**
 * A workspace the current user belongs to, as returned on the session.
 * Defined locally (not imported from `store/api.ts`) so this slice never
 * imports the generated API module — that keeps `empty-api.ts -> slices/auth.ts`
 * a one-way edge instead of a cycle (see empty-api.ts for the other half).
 */
export interface Membership {
  workspace_id: string
  workspace_name: string
  role: string
  /**
   * When THIS workspace finished first-run onboarding, or null while pending.
   * Per-workspace, so switching into a freshly created one can gate the onboarding
   * overlay without an extra request. Optional here (unlike the generated
   * `Membership`, where the API always sends it) so a session from an API that
   * predates onboarding still populates the slice instead of failing to type-check
   * at the boundary.
   */
  onboarding_completed_at?: string | null
}

/** Shape of a login/register/refresh/switch-workspace response body. */
export interface SessionResponse {
  access_token: string
  expires_in: number
  user_id: string
  active_workspace_id: string
  role: string
  memberships: Membership[]
  /**
   * Identity fields, kept optional here even though the API now always sends
   * `email` on a session: a discoverable passkey login and the login form both
   * seed identity from other sources (see setUserIdentity), and `name` still has
   * no server-side home. Optional means this slice accepts a session body from
   * any of those paths without a widening cast at the boundary.
   */
  email?: string
  name?: string
}

/**
 * Session state: the access token and the ids it encodes. Held in memory ONLY
 * (never persisted — see `store/index.ts`'s persist whitelist): the httpOnly
 * refresh cookie is the source of truth across reloads, restored via the
 * silent-refresh bootstrap (`features/auth/use-auth-bootstrap.ts`). Attached
 * as a Bearer token by the RTK Query base query in `store/empty-api.ts`.
 */
export interface AuthState {
  accessToken: string | null
  userId: string | null
  userEmail: string | null
  userName: string | null
  activeWorkspaceId: string | null
  role: string | null
  memberships: Membership[]
  /**
   * 'idle': not yet attempted the silent-refresh bootstrap.
   * 'authed': a valid in-memory session is present.
   * 'anon': bootstrap (or a reauth attempt) concluded there is no session.
   */
  status: 'idle' | 'authed' | 'anon'
}

const initialState: AuthState = {
  accessToken: null,
  userId: null,
  userEmail: null,
  userName: null,
  activeWorkspaceId: null,
  role: null,
  memberships: [],
  status: 'idle',
}

const authSlice = createSlice({
  name: 'auth',
  initialState,
  reducers: {
    setSession: (state, action: PayloadAction<SessionResponse>) => {
      state.accessToken = action.payload.access_token
      state.userId = action.payload.user_id
      state.activeWorkspaceId = action.payload.active_workspace_id
      state.role = action.payload.role
      state.memberships = action.payload.memberships
      if (action.payload.email !== undefined) state.userEmail = action.payload.email
      if (action.payload.name !== undefined) state.userName = action.payload.name
      state.status = 'authed'
    },
    setUserIdentity: (
      state,
      action: PayloadAction<{ email?: string | null; name?: string | null }>,
    ) => {
      if (action.payload.email !== undefined) state.userEmail = action.payload.email
      if (action.payload.name !== undefined) state.userName = action.payload.name
    },
    clearSession: (state) => {
      state.accessToken = null
      state.userId = null
      state.userEmail = null
      state.userName = null
      state.activeWorkspaceId = null
      state.role = null
      state.memberships = []
      state.status = 'anon'
    },
    /**
     * Renames the active workspace in the membership list. The session body is
     * the only source of workspace names in the client (the header's workspace
     * switcher reads them straight off `memberships`), so a rename that only
     * lands server-side leaves the switcher showing the old name until the next
     * full session refresh — which for the onboarding overlay is the very name
     * the user just replaced.
     */
    renameActiveWorkspace: (state, action: PayloadAction<{ name: string }>) => {
      const active = state.memberships.find((m) => m.workspace_id === state.activeWorkspaceId)
      if (active) active.workspace_name = action.payload.name
    },
    setActiveWorkspace: (
      state,
      action: PayloadAction<{ activeWorkspaceId: string; role: string; accessToken: string }>,
    ) => {
      state.activeWorkspaceId = action.payload.activeWorkspaceId
      state.role = action.payload.role
      state.accessToken = action.payload.accessToken
    },
  },
})

export const { setSession, setUserIdentity, clearSession, renameActiveWorkspace, setActiveWorkspace } =
  authSlice.actions
export default authSlice.reducer
