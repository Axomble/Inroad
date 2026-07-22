// Auth feature endpoints. The `Session` tag is intentionally minimal — most
// auth mutations don't invalidate cache directly, because a workspace switch
// resets the whole api state (see workspace-switcher.tsx) and login/logout
// dispatch setSession/clearSession which components observe via the auth slice.
// `authMe` is tagged so it refetches after a session change if any component
// happens to be subscribed.
import { api } from '@/store/api'

const authApi = api.enhanceEndpoints({
  addTagTypes: ['Session'],
  endpoints: {
    authMe: {
      providesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
    authLogin: {
      invalidatesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
    authRegister: {
      invalidatesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
    authRefresh: {
      invalidatesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
    authLogout: {
      invalidatesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
    authLogoutAll: {
      invalidatesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
    authSwitchWorkspace: {
      invalidatesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
    // Marks the account verified server-side — refetch `authMe` so the
    // unverified banner (subscribed everywhere in the app shell) clears
    // immediately instead of waiting for its next natural refetch.
    authVerifyEmail: {
      invalidatesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
    authAcceptInvite: {
      invalidatesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
  },
})

export const {
  useAuthRegisterMutation,
  useAuthLoginMutation,
  useAuthRefreshMutation,
  useAuthLogoutMutation,
  useAuthMeQuery,
  useAuthLogoutAllMutation,
  useAuthSwitchWorkspaceMutation,
  useAuthVerifyEmailMutation,
  useAuthResendVerificationMutation,
  useAuthForgotPasswordMutation,
  useAuthResetPasswordMutation,
  useAuthAcceptInviteMutation,
} = authApi
