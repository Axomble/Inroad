import { createSlice, type PayloadAction } from '@reduxjs/toolkit'

/**
 * Session state: the JWT and the ids it encodes. Persisted (whitelisted in the
 * store) so a reload keeps you signed in; attached as a Bearer token by the
 * RTK Query base query in `store/empty-api.ts`.
 */
interface AuthState {
  token: string | null
  userId: string | null
  workspaceId: string | null
}

const initialState: AuthState = { token: null, userId: null, workspaceId: null }

const authSlice = createSlice({
  name: 'auth',
  initialState,
  reducers: {
    setSession: (
      state,
      action: PayloadAction<{ token: string; userId: string; workspaceId: string }>,
    ) => {
      state.token = action.payload.token
      state.userId = action.payload.userId
      state.workspaceId = action.payload.workspaceId
    },
    clearSession: (state) => {
      state.token = null
      state.userId = null
      state.workspaceId = null
    },
  },
})

export const { setSession, clearSession } = authSlice.actions
export default authSlice.reducer
