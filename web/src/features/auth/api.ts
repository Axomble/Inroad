// Auth feature endpoints. The `Session` tag is intentionally minimal — most
// auth mutations don't invalidate cache directly, because a workspace switch
// resets the whole api state (see workspace-switcher.tsx) and login/logout
// dispatch setSession/clearSession which components observe via the auth slice.
// `authMe` is tagged so it refetches after a session change if any component
// happens to be subscribed.
import { api } from '@/store/api'

const authApi = api.enhanceEndpoints({
  addTagTypes: ['Session', 'Sessions', 'TwoFactor', 'Passkeys', 'ApiKeys'],
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
    // Naming a workspace flips `onboarding_completed` on the session, and that
    // flag is what dismisses the first-run overlay — so refetch `authMe` rather
    // than letting the overlay close itself on a local success flag.
    completeWorkspaceOnboarding: {
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
    // Passkeys (P4 auth hardening). The list is the single source of truth for
    // the Security-page section; registering (finish) or deleting a passkey
    // invalidates the `Passkeys` tag so the list refetches itself. Register/
    // login BEGIN stage nothing durable, so they invalidate nothing.
    authPasskeyList: {
      providesTags: (result) =>
        result
          ? [
              ...result.passkeys.map((p) => ({ type: 'Passkeys' as const, id: p.id })),
              { type: 'Passkeys' as const, id: 'LIST' },
            ]
          : [{ type: 'Passkeys' as const, id: 'LIST' }],
    },
    authPasskeyRegisterFinish: {
      invalidatesTags: [{ type: 'Passkeys', id: 'LIST' }],
    },
    authPasskeyDelete: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'Passkeys', id: arg.id },
        { type: 'Passkeys', id: 'LIST' },
      ],
    },
    // A discoverable passkey login mints a session exactly like authLogin.
    authPasskeyLoginFinish: {
      invalidatesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
    // Email-OTP login (P5). Verify mints a session (or a 2FA challenge) like a
    // password login; start is anti-enumeration and durable-state-free.
    authEmailOtpVerify: {
      invalidatesTags: [{ type: 'Session', id: 'CURRENT' }],
    },
    // API keys (P6, admin-gated). The list drives the settings panel; creating
    // or revoking a key invalidates the `ApiKeys` tag so the list refetches.
    authApiKeyList: {
      providesTags: (result) =>
        result
          ? [
              ...result.api_keys.map((k) => ({ type: 'ApiKeys' as const, id: k.id })),
              { type: 'ApiKeys' as const, id: 'LIST' },
            ]
          : [{ type: 'ApiKeys' as const, id: 'LIST' }],
    },
    authApiKeyCreate: {
      invalidatesTags: [{ type: 'ApiKeys', id: 'LIST' }],
    },
    authApiKeyRevoke: {
      invalidatesTags: (_result, _error, arg) => [
        { type: 'ApiKeys', id: arg.id },
        { type: 'ApiKeys', id: 'LIST' },
      ],
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
  useAuthPasskeyListQuery,
  useAuthPasskeyDeleteMutation,
  useAuthPasskeyRegisterBeginMutation,
  useAuthPasskeyRegisterFinishMutation,
  useAuthPasskeyLoginBeginMutation,
  useAuthPasskeyLoginFinishMutation,
  useAuthEmailOtpStartMutation,
  useAuthEmailOtpVerifyMutation,
  useAuthApiKeyCreateMutation,
  useAuthApiKeyListQuery,
  useAuthApiKeyRevokeMutation,
  // The generated client also carries `authGoogleSignInRedirect`,
  // `authGoogleSignInStart` and `authGoogleSignInCallback`. None is re-exported:
  // the redirect and the callback are browser navigations (fetching them from the
  // SPA does nothing useful — see `google-signin-url.ts` and
  // `google-callback-page.tsx`), and the POST start exists only for the
  // invite-with-Google flow and a 501 capability probe, neither of which is wired.
  useCompleteWorkspaceOnboardingMutation,
} = authApi
