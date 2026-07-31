// Auth feature endpoints. The `Session` tag is intentionally minimal — most
// auth mutations don't invalidate cache directly, because a workspace switch
// resets the whole api state (see workspace-switcher.tsx) and login/logout
// dispatch setSession/clearSession which components observe via the auth slice.
// `authMe` is tagged so it refetches after a session change if any component
// happens to be subscribed.
import { api } from '@/store/api'

const authApi = api.enhanceEndpoints({
  addTagTypes: ['Session', 'Sessions', 'TwoFactor'],
  endpoints: {
    authMe: {
      providesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
    // The revocable-session list (P1 auth hardening). Tagged so revoking one
    // session or signing out everywhere else refetches the list automatically —
    // no hand-rolled refetch() in the screen.
    authListSessions: {
      providesTags: (result) =>
        result
          ? [
              ...result.sessions.map((s) => ({ type: 'Sessions' as const, id: s.id })),
              { type: 'Sessions' as const, id: 'LIST' },
            ]
          : [{ type: 'Sessions' as const, id: 'LIST' }],
    },
    authRevokeSession: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Sessions', id: arg.id },
        { type: 'Sessions', id: 'LIST' },
      ],
    },
    authRevokeOtherSessions: {
      invalidatesTags: [{ type: 'Sessions', id: 'LIST' }],
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
    // Two-factor (P2 auth hardening). The status query is the single source of
    // truth for the settings panel; confirm (activate) and disable both flip
    // `totp_enabled`, so they invalidate the `TwoFactor` tag to refetch it.
    // Enroll only stages an UNCONFIRMED secret — it doesn't change status, so
    // it invalidates nothing. Disabling also revokes the caller's other
    // sessions server-side, so it invalidates the `Sessions` list too.
    authTwoFactorStatus: {
      providesTags: [{ type: 'TwoFactor', id: 'STATUS' }],
    },
    authTwoFactorConfirm: {
      invalidatesTags: [{ type: 'TwoFactor', id: 'STATUS' }],
    },
    authTwoFactorDisable: {
      invalidatesTags: [
        { type: 'TwoFactor', id: 'STATUS' },
        { type: 'Sessions', id: 'LIST' },
      ],
    },
    // Completing the 2FA login gate mints a session, exactly like authLogin.
    authTwoFactorVerify: {
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
  useAuthListSessionsQuery,
  useAuthRevokeSessionMutation,
  useAuthRevokeOtherSessionsMutation,
  useAuthTwoFactorStatusQuery,
  useAuthTwoFactorEnrollMutation,
  useAuthTwoFactorConfirmMutation,
  useAuthTwoFactorDisableMutation,
  useAuthTwoFactorVerifyMutation,
} = authApi
