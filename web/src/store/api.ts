import { emptyApi as api } from "./empty-api";
const injectedRtkApi = api.injectEndpoints({
  endpoints: (build) => ({
    authRegister: build.mutation<AuthRegisterApiResponse, AuthRegisterApiArg>({
      query: (queryArg) => ({
        url: `/auth/register`,
        method: "POST",
        body: queryArg.registerRequest,
        headers: {
          "X-Captcha-Token": queryArg["X-Captcha-Token"],
        },
      }),
    }),
    authLogin: build.mutation<AuthLoginApiResponse, AuthLoginApiArg>({
      query: (queryArg) => ({
        url: `/auth/login`,
        method: "POST",
        body: queryArg.loginRequest,
        headers: {
          "X-Captcha-Token": queryArg["X-Captcha-Token"],
        },
      }),
    }),
    authRefresh: build.mutation<AuthRefreshApiResponse, AuthRefreshApiArg>({
      query: () => ({ url: `/auth/refresh`, method: "POST" }),
    }),
    authLogout: build.mutation<AuthLogoutApiResponse, AuthLogoutApiArg>({
      query: () => ({ url: `/auth/logout`, method: "POST" }),
    }),
    authMe: build.query<AuthMeApiResponse, AuthMeApiArg>({
      query: () => ({ url: `/auth/me` }),
    }),
    authLogoutAll: build.mutation<
      AuthLogoutAllApiResponse,
      AuthLogoutAllApiArg
    >({
      query: () => ({ url: `/auth/logout-all`, method: "POST" }),
    }),
    authSwitchWorkspace: build.mutation<
      AuthSwitchWorkspaceApiResponse,
      AuthSwitchWorkspaceApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/switch-workspace`,
        method: "POST",
        body: queryArg.switchWorkspaceRequest,
      }),
    }),
    authListSessions: build.query<
      AuthListSessionsApiResponse,
      AuthListSessionsApiArg
    >({
      query: () => ({ url: `/auth/sessions` }),
    }),
    authRevokeOtherSessions: build.mutation<
      AuthRevokeOtherSessionsApiResponse,
      AuthRevokeOtherSessionsApiArg
    >({
      query: () => ({ url: `/auth/sessions/revoke-others`, method: "POST" }),
    }),
    authRevokeSession: build.mutation<
      AuthRevokeSessionApiResponse,
      AuthRevokeSessionApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/sessions/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    authVerifyEmail: build.mutation<
      AuthVerifyEmailApiResponse,
      AuthVerifyEmailApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/verify-email`,
        method: "POST",
        body: queryArg.verifyEmailRequest,
      }),
    }),
    authResendVerification: build.mutation<
      AuthResendVerificationApiResponse,
      AuthResendVerificationApiArg
    >({
      query: () => ({ url: `/auth/verify-email/resend`, method: "POST" }),
    }),
    authForgotPassword: build.mutation<
      AuthForgotPasswordApiResponse,
      AuthForgotPasswordApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/password/forgot`,
        method: "POST",
        body: queryArg.forgotPasswordRequest,
      }),
    }),
    authResetPassword: build.mutation<
      AuthResetPasswordApiResponse,
      AuthResetPasswordApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/password/reset`,
        method: "POST",
        body: queryArg.resetPasswordRequest,
      }),
    }),
    authAcceptInvite: build.mutation<
      AuthAcceptInviteApiResponse,
      AuthAcceptInviteApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/invites/accept`,
        method: "POST",
        body: queryArg.acceptInviteRequest,
      }),
    }),
    authTwoFactorStatus: build.query<
      AuthTwoFactorStatusApiResponse,
      AuthTwoFactorStatusApiArg
    >({
      query: () => ({ url: `/auth/2fa` }),
    }),
    authTwoFactorEnroll: build.mutation<
      AuthTwoFactorEnrollApiResponse,
      AuthTwoFactorEnrollApiArg
    >({
      query: () => ({ url: `/auth/2fa/totp`, method: "POST" }),
    }),
    authTwoFactorDisable: build.mutation<
      AuthTwoFactorDisableApiResponse,
      AuthTwoFactorDisableApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/2fa/totp`,
        method: "DELETE",
        body: queryArg.twoFactorCodeRequest,
      }),
    }),
    authTwoFactorConfirm: build.mutation<
      AuthTwoFactorConfirmApiResponse,
      AuthTwoFactorConfirmApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/2fa/totp/confirm`,
        method: "POST",
        body: queryArg.twoFactorCodeRequest,
      }),
    }),
    authTwoFactorVerify: build.mutation<
      AuthTwoFactorVerifyApiResponse,
      AuthTwoFactorVerifyApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/2fa/verify`,
        method: "POST",
        body: queryArg.twoFactorVerifyRequest,
      }),
    }),
    authPasskeyList: build.query<
      AuthPasskeyListApiResponse,
      AuthPasskeyListApiArg
    >({
      query: () => ({ url: `/auth/passkeys` }),
    }),
    authPasskeyDelete: build.mutation<
      AuthPasskeyDeleteApiResponse,
      AuthPasskeyDeleteApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/passkeys/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    authPasskeyRegisterBegin: build.mutation<
      AuthPasskeyRegisterBeginApiResponse,
      AuthPasskeyRegisterBeginApiArg
    >({
      query: () => ({ url: `/auth/passkeys/register/begin`, method: "POST" }),
    }),
    authPasskeyRegisterFinish: build.mutation<
      AuthPasskeyRegisterFinishApiResponse,
      AuthPasskeyRegisterFinishApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/passkeys/register/finish`,
        method: "POST",
        body: queryArg.passkeyFinishRequest,
      }),
    }),
    authPasskeyLoginBegin: build.mutation<
      AuthPasskeyLoginBeginApiResponse,
      AuthPasskeyLoginBeginApiArg
    >({
      query: () => ({ url: `/auth/passkeys/login/begin`, method: "POST" }),
    }),
    authPasskeyLoginFinish: build.mutation<
      AuthPasskeyLoginFinishApiResponse,
      AuthPasskeyLoginFinishApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/passkeys/login/finish`,
        method: "POST",
        body: queryArg.passkeyFinishRequest,
      }),
    }),
    authEmailOtpStart: build.mutation<
      AuthEmailOtpStartApiResponse,
      AuthEmailOtpStartApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/email-otp/start`,
        method: "POST",
        body: queryArg.emailOtpStartRequest,
        headers: {
          "X-Captcha-Token": queryArg["X-Captcha-Token"],
        },
      }),
    }),
    authEmailOtpVerify: build.mutation<
      AuthEmailOtpVerifyApiResponse,
      AuthEmailOtpVerifyApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/email-otp/verify`,
        method: "POST",
        body: queryArg.emailOtpVerifyRequest,
      }),
    }),
    authApiKeyCreate: build.mutation<
      AuthApiKeyCreateApiResponse,
      AuthApiKeyCreateApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/api-keys`,
        method: "POST",
        body: queryArg.apiKeyCreateRequest,
      }),
    }),
    authApiKeyList: build.query<
      AuthApiKeyListApiResponse,
      AuthApiKeyListApiArg
    >({
      query: () => ({ url: `/auth/api-keys` }),
    }),
    authApiKeyRevoke: build.mutation<
      AuthApiKeyRevokeApiResponse,
      AuthApiKeyRevokeApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/api-keys/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    createWorkspaceInvite: build.mutation<
      CreateWorkspaceInviteApiResponse,
      CreateWorkspaceInviteApiArg
    >({
      query: (queryArg) => ({
        url: `/workspaces/${queryArg.id}/invites`,
        method: "POST",
        body: queryArg.createInviteRequest,
      }),
    }),
    listWorkspaceInvites: build.query<
      ListWorkspaceInvitesApiResponse,
      ListWorkspaceInvitesApiArg
    >({
      query: (queryArg) => ({ url: `/workspaces/${queryArg.id}/invites` }),
    }),
    revokeWorkspaceInvite: build.mutation<
      RevokeWorkspaceInviteApiResponse,
      RevokeWorkspaceInviteApiArg
    >({
      query: (queryArg) => ({
        url: `/workspaces/${queryArg.id}/invites/${queryArg.inviteId}`,
        method: "DELETE",
      }),
    }),
    listMailboxes: build.query<ListMailboxesApiResponse, ListMailboxesApiArg>({
      query: () => ({ url: `/mailboxes` }),
    }),
    connectMailbox: build.mutation<
      ConnectMailboxApiResponse,
      ConnectMailboxApiArg
    >({
      query: (queryArg) => ({
        url: `/mailboxes`,
        method: "POST",
        body: queryArg.connectMailboxRequest,
      }),
    }),
    getMailbox: build.query<GetMailboxApiResponse, GetMailboxApiArg>({
      query: (queryArg) => ({ url: `/mailboxes/${queryArg.id}` }),
    }),
    deleteMailbox: build.mutation<
      DeleteMailboxApiResponse,
      DeleteMailboxApiArg
    >({
      query: (queryArg) => ({
        url: `/mailboxes/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    pauseMailbox: build.mutation<PauseMailboxApiResponse, PauseMailboxApiArg>({
      query: (queryArg) => ({
        url: `/mailboxes/${queryArg.id}/pause`,
        method: "POST",
      }),
    }),
    resumeMailbox: build.mutation<
      ResumeMailboxApiResponse,
      ResumeMailboxApiArg
    >({
      query: (queryArg) => ({
        url: `/mailboxes/${queryArg.id}/resume`,
        method: "POST",
      }),
    }),
    getMailboxWarmup: build.query<
      GetMailboxWarmupApiResponse,
      GetMailboxWarmupApiArg
    >({
      query: (queryArg) => ({ url: `/mailboxes/${queryArg.id}/warmup` }),
    }),
    enableMailboxWarmup: build.mutation<
      EnableMailboxWarmupApiResponse,
      EnableMailboxWarmupApiArg
    >({
      query: (queryArg) => ({
        url: `/mailboxes/${queryArg.id}/warmup`,
        method: "PUT",
        body: queryArg.warmupSettings,
      }),
    }),
    disableMailboxWarmup: build.mutation<
      DisableMailboxWarmupApiResponse,
      DisableMailboxWarmupApiArg
    >({
      query: (queryArg) => ({
        url: `/mailboxes/${queryArg.id}/warmup`,
        method: "DELETE",
      }),
    }),
    getWarmupOverview: build.query<
      GetWarmupOverviewApiResponse,
      GetWarmupOverviewApiArg
    >({
      query: () => ({ url: `/warmup/overview` }),
    }),
    getPulse: build.query<GetPulseApiResponse, GetPulseApiArg>({
      query: () => ({ url: `/pulse` }),
    }),
    listLists: build.query<ListListsApiResponse, ListListsApiArg>({
      query: () => ({ url: `/lists` }),
    }),
    createList: build.mutation<CreateListApiResponse, CreateListApiArg>({
      query: (queryArg) => ({
        url: `/lists`,
        method: "POST",
        body: queryArg.body,
      }),
    }),
    importContacts: build.mutation<
      ImportContactsApiResponse,
      ImportContactsApiArg
    >({
      query: (queryArg) => ({
        url: `/contacts/import`,
        method: "POST",
        body: queryArg.body,
        params: {
          list: queryArg.list,
        },
      }),
    }),
    listContacts: build.query<ListContactsApiResponse, ListContactsApiArg>({
      query: (queryArg) => ({
        url: `/contacts`,
        params: {
          list: queryArg.list,
          q: queryArg.q,
          sort: queryArg.sort,
          cursor: queryArg.cursor,
          limit: queryArg.limit,
        },
      }),
    }),
    listSendingDomains: build.query<
      ListSendingDomainsApiResponse,
      ListSendingDomainsApiArg
    >({
      query: () => ({ url: `/sending-domains` }),
    }),
    checkSendingDomain: build.mutation<
      CheckSendingDomainApiResponse,
      CheckSendingDomainApiArg
    >({
      query: (queryArg) => ({
        url: `/sending-domains/${queryArg.domain}/check`,
        method: "POST",
      }),
    }),
    getWorkspaceDeliverability: build.query<
      GetWorkspaceDeliverabilityApiResponse,
      GetWorkspaceDeliverabilityApiArg
    >({
      query: () => ({ url: `/deliverability` }),
    }),
    getCampaignDeliverability: build.query<
      GetCampaignDeliverabilityApiResponse,
      GetCampaignDeliverabilityApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/deliverability`,
      }),
    }),
    updateCampaignGuardrails: build.mutation<
      UpdateCampaignGuardrailsApiResponse,
      UpdateCampaignGuardrailsApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/guardrails`,
        method: "PUT",
        body: queryArg.campaignGuardrails,
      }),
    }),
    ingestDeliverabilityEvent: build.mutation<
      IngestDeliverabilityEventApiResponse,
      IngestDeliverabilityEventApiArg
    >({
      query: (queryArg) => ({
        url: `/deliverability/events`,
        method: "POST",
        body: queryArg.deliverabilityEvent,
      }),
    }),
    listCampaigns: build.query<ListCampaignsApiResponse, ListCampaignsApiArg>({
      query: () => ({ url: `/campaigns` }),
    }),
    createCampaign: build.mutation<
      CreateCampaignApiResponse,
      CreateCampaignApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns`,
        method: "POST",
        body: queryArg.createCampaignRequest,
      }),
    }),
    getCampaign: build.query<GetCampaignApiResponse, GetCampaignApiArg>({
      query: (queryArg) => ({ url: `/campaigns/${queryArg.id}` }),
    }),
    renameCampaign: build.mutation<
      RenameCampaignApiResponse,
      RenameCampaignApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}`,
        method: "PUT",
        body: queryArg.renameCampaignRequest,
      }),
    }),
    deleteCampaign: build.mutation<
      DeleteCampaignApiResponse,
      DeleteCampaignApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    pauseCampaign: build.mutation<
      PauseCampaignApiResponse,
      PauseCampaignApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/pause`,
        method: "POST",
      }),
    }),
    resumeCampaign: build.mutation<
      ResumeCampaignApiResponse,
      ResumeCampaignApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/resume`,
        method: "POST",
      }),
    }),
    updateCampaignTracking: build.mutation<
      UpdateCampaignTrackingApiResponse,
      UpdateCampaignTrackingApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/tracking`,
        method: "PUT",
        body: queryArg.updateCampaignTrackingRequest,
      }),
    }),
    getCampaignSchedule: build.query<
      GetCampaignScheduleApiResponse,
      GetCampaignScheduleApiArg
    >({
      query: (queryArg) => ({ url: `/campaigns/${queryArg.id}/schedule` }),
    }),
    updateCampaignSchedule: build.mutation<
      UpdateCampaignScheduleApiResponse,
      UpdateCampaignScheduleApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/schedule`,
        method: "PUT",
        body: queryArg.campaignScheduleRequest,
      }),
    }),
    getCampaignSenders: build.query<
      GetCampaignSendersApiResponse,
      GetCampaignSendersApiArg
    >({
      query: (queryArg) => ({ url: `/campaigns/${queryArg.id}/senders` }),
    }),
    updateCampaignSenders: build.mutation<
      UpdateCampaignSendersApiResponse,
      UpdateCampaignSendersApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/senders`,
        method: "PUT",
        body: queryArg.campaignSenderPoolRequest,
      }),
    }),
    listCampaignEnrollments: build.query<
      ListCampaignEnrollmentsApiResponse,
      ListCampaignEnrollmentsApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/enrollments`,
        params: {
          limit: queryArg.limit,
          offset: queryArg.offset,
        },
      }),
    }),
    listSteps: build.query<ListStepsApiResponse, ListStepsApiArg>({
      query: (queryArg) => ({ url: `/campaigns/${queryArg.id}/steps` }),
    }),
    createStep: build.mutation<CreateStepApiResponse, CreateStepApiArg>({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/steps`,
        method: "POST",
        body: queryArg.stepRequest,
      }),
    }),
    updateStep: build.mutation<UpdateStepApiResponse, UpdateStepApiArg>({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/steps/${queryArg.stepId}`,
        method: "PUT",
        body: queryArg.stepRequest,
      }),
    }),
    deleteStep: build.mutation<DeleteStepApiResponse, DeleteStepApiArg>({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/steps/${queryArg.stepId}`,
        method: "DELETE",
      }),
    }),
    reorderSteps: build.mutation<ReorderStepsApiResponse, ReorderStepsApiArg>({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/steps/reorder`,
        method: "POST",
        body: queryArg.reorderStepsRequest,
      }),
    }),
    launchCampaign: build.mutation<
      LaunchCampaignApiResponse,
      LaunchCampaignApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/launch`,
        method: "POST",
      }),
    }),
    unsubscribeConfirmPage: build.query<
      UnsubscribeConfirmPageApiResponse,
      UnsubscribeConfirmPageApiArg
    >({
      query: (queryArg) => ({ url: `/u/${queryArg.token}` }),
    }),
    unsubscribe: build.mutation<UnsubscribeApiResponse, UnsubscribeApiArg>({
      query: (queryArg) => ({ url: `/u/${queryArg.token}`, method: "POST" }),
    }),
    trackOpen: build.query<TrackOpenApiResponse, TrackOpenApiArg>({
      query: (queryArg) => ({ url: `/t/o/${queryArg.token}.gif` }),
    }),
    trackClick: build.query<TrackClickApiResponse, TrackClickApiArg>({
      query: (queryArg) => ({ url: `/t/c/${queryArg.token}` }),
    }),
    oauth2Register: build.mutation<
      Oauth2RegisterApiResponse,
      Oauth2RegisterApiArg
    >({
      query: (queryArg) => ({
        url: `/oauth2/register`,
        method: "POST",
        body: queryArg.oAuth2RegisterRequest,
      }),
    }),
    oauth2Authorize: build.query<
      Oauth2AuthorizeApiResponse,
      Oauth2AuthorizeApiArg
    >({
      query: (queryArg) => ({
        url: `/oauth2/authorize`,
        params: {
          response_type: queryArg.responseType,
          client_id: queryArg.clientId,
          redirect_uri: queryArg.redirectUri,
          scope: queryArg.scope,
          state: queryArg.state,
          code_challenge: queryArg.codeChallenge,
          code_challenge_method: queryArg.codeChallengeMethod,
        },
      }),
    }),
    oauth2ConsentData: build.query<
      Oauth2ConsentDataApiResponse,
      Oauth2ConsentDataApiArg
    >({
      query: (queryArg) => ({ url: `/oauth2/consent/${queryArg.consentId}` }),
    }),
    oauth2ConsentDecide: build.mutation<
      Oauth2ConsentDecideApiResponse,
      Oauth2ConsentDecideApiArg
    >({
      query: (queryArg) => ({
        url: `/oauth2/consent`,
        method: "POST",
        body: queryArg.oAuth2ConsentDecision,
      }),
    }),
    oauth2ListClients: build.query<
      Oauth2ListClientsApiResponse,
      Oauth2ListClientsApiArg
    >({
      query: () => ({ url: `/oauth2/clients` }),
    }),
    oauth2RevokeClient: build.mutation<
      Oauth2RevokeClientApiResponse,
      Oauth2RevokeClientApiArg
    >({
      query: (queryArg) => ({
        url: `/oauth2/clients/${queryArg.clientId}`,
        method: "DELETE",
      }),
    }),
    oauth2Token: build.mutation<Oauth2TokenApiResponse, Oauth2TokenApiArg>({
      query: (queryArg) => ({
        url: `/oauth2/token`,
        method: "POST",
        body: queryArg.oAuth2TokenRequest,
      }),
    }),
    oauth2Introspect: build.mutation<
      Oauth2IntrospectApiResponse,
      Oauth2IntrospectApiArg
    >({
      query: (queryArg) => ({
        url: `/oauth2/introspect`,
        method: "POST",
        body: queryArg.oAuth2IntrospectRequest,
      }),
    }),
    oauth2Revoke: build.mutation<Oauth2RevokeApiResponse, Oauth2RevokeApiArg>({
      query: (queryArg) => ({
        url: `/oauth2/revoke`,
        method: "POST",
        body: queryArg.oAuth2RevokeRequest,
      }),
    }),
  }),
  overrideExisting: false,
});
export { injectedRtkApi as api };
export type AuthRegisterApiResponse = /** status 200 Session */ SessionResponse;
export type AuthRegisterApiArg = {
  /** Client-solved captcha (Cloudflare Turnstile) token. Required ONLY when the server is configured with a captcha secret (INROAD_TURNSTILE_SECRET); on such a server a missing or invalid token is rejected 403. On a server without a captcha configured the header is ignored. */
  "X-Captcha-Token"?: string;
  registerRequest: RegisterRequest;
};
export type AuthLoginApiResponse =
  | /** status 200 A session (SessionResponse) for a user without a confirmed second factor, OR a single-use 2FA challenge (TwoFactorRequiredResponse, no tokens) for a user with 2FA enabled — the caller must then POST /auth/2fa/verify to obtain a session. */ SessionResponse
  | TwoFactorRequiredResponse;
export type AuthLoginApiArg = {
  /** Client-solved captcha (Cloudflare Turnstile) token. Required ONLY when the server is configured with a captcha secret (INROAD_TURNSTILE_SECRET); on such a server a missing or invalid token is rejected 403. On a server without a captcha configured the header is ignored. */
  "X-Captcha-Token"?: string;
  loginRequest: LoginRequest;
};
export type AuthRefreshApiResponse =
  /** status 200 Refreshed session */ SessionResponse;
export type AuthRefreshApiArg = void;
export type AuthLogoutApiResponse = /** status 200 Logged out */ {
  status?: string;
};
export type AuthLogoutApiArg = void;
export type AuthMeApiResponse = /** status 200 Current identity */ MeResponse;
export type AuthMeApiArg = void;
export type AuthLogoutAllApiResponse =
  /** status 200 Logged out of all sessions */ {
    status?: string;
  };
export type AuthLogoutAllApiArg = void;
export type AuthSwitchWorkspaceApiResponse =
  /** status 200 Switched active workspace */ SwitchWorkspaceResponse;
export type AuthSwitchWorkspaceApiArg = {
  switchWorkspaceRequest: SwitchWorkspaceRequest;
};
export type AuthListSessionsApiResponse =
  /** status 200 The caller's active sessions (current one flagged) */ SessionListResponse;
export type AuthListSessionsApiArg = void;
export type AuthRevokeOtherSessionsApiResponse =
  /** status 200 Revoked every session except the current one */ RevokeOthersResponse;
export type AuthRevokeOtherSessionsApiArg = void;
export type AuthRevokeSessionApiResponse = unknown;
export type AuthRevokeSessionApiArg = {
  id: string;
};
export type AuthVerifyEmailApiResponse = unknown;
export type AuthVerifyEmailApiArg = {
  verifyEmailRequest: VerifyEmailRequest;
};
export type AuthResendVerificationApiResponse = unknown;
export type AuthResendVerificationApiArg = void;
export type AuthForgotPasswordApiResponse = unknown;
export type AuthForgotPasswordApiArg = {
  forgotPasswordRequest: ForgotPasswordRequest;
};
export type AuthResetPasswordApiResponse = unknown;
export type AuthResetPasswordApiArg = {
  resetPasswordRequest: ResetPasswordRequest;
};
export type AuthAcceptInviteApiResponse =
  /** status 200 Session */ SessionResponse;
export type AuthAcceptInviteApiArg = {
  acceptInviteRequest: AcceptInviteRequest;
};
export type AuthTwoFactorStatusApiResponse =
  /** status 200 2FA status for the caller */ TwoFactorStatusResponse;
export type AuthTwoFactorStatusApiArg = void;
export type AuthTwoFactorEnrollApiResponse =
  /** status 200 Pending enrollment secret */ TwoFactorEnrollResponse;
export type AuthTwoFactorEnrollApiArg = void;
export type AuthTwoFactorDisableApiResponse = unknown;
export type AuthTwoFactorDisableApiArg = {
  twoFactorCodeRequest: TwoFactorCodeRequest;
};
export type AuthTwoFactorConfirmApiResponse =
  /** status 200 Two-factor enabled; recovery codes returned once */ TwoFactorConfirmResponse;
export type AuthTwoFactorConfirmApiArg = {
  twoFactorCodeRequest: TwoFactorCodeRequest;
};
export type AuthTwoFactorVerifyApiResponse =
  /** status 200 Session */ SessionResponse;
export type AuthTwoFactorVerifyApiArg = {
  twoFactorVerifyRequest: TwoFactorVerifyRequest;
};
export type AuthPasskeyListApiResponse =
  /** status 200 The caller's passkeys */ PasskeyListResponse;
export type AuthPasskeyListApiArg = void;
export type AuthPasskeyDeleteApiResponse = unknown;
export type AuthPasskeyDeleteApiArg = {
  id: string;
};
export type AuthPasskeyRegisterBeginApiResponse =
  /** status 200 Registration options + ceremony session id */ PasskeyBeginResponse;
export type AuthPasskeyRegisterBeginApiArg = void;
export type AuthPasskeyRegisterFinishApiResponse = unknown;
export type AuthPasskeyRegisterFinishApiArg = {
  passkeyFinishRequest: PasskeyFinishRequest;
};
export type AuthPasskeyLoginBeginApiResponse =
  /** status 200 Assertion options + ceremony session id */ PasskeyBeginResponse;
export type AuthPasskeyLoginBeginApiArg = void;
export type AuthPasskeyLoginFinishApiResponse =
  /** status 200 Session */ SessionResponse;
export type AuthPasskeyLoginFinishApiArg = {
  passkeyFinishRequest: PasskeyFinishRequest;
};
export type AuthEmailOtpStartApiResponse =
  /** status 200 Generic acknowledgement (identical for existing and non-existing emails) */ EmailOtpStartResponse;
export type AuthEmailOtpStartApiArg = {
  /** Client-solved captcha (Cloudflare Turnstile) token. Required ONLY when the server is configured with a captcha secret (INROAD_TURNSTILE_SECRET); on such a server a missing or invalid token is rejected 403. On a server without a captcha configured the header is ignored. */
  "X-Captcha-Token"?: string;
  emailOtpStartRequest: EmailOtpStartRequest;
};
export type AuthEmailOtpVerifyApiResponse =
  | /** status 200 A session (SessionResponse) for a user without a confirmed second factor, OR a single-use 2FA challenge (TwoFactorRequiredResponse, no tokens) for a user with 2FA enabled. */ SessionResponse
  | TwoFactorRequiredResponse;
export type AuthEmailOtpVerifyApiArg = {
  emailOtpVerifyRequest: EmailOtpVerifyRequest;
};
export type AuthApiKeyCreateApiResponse =
  /** status 201 The created key metadata plus the one-time token */ ApiKeyCreateResponse;
export type AuthApiKeyCreateApiArg = {
  apiKeyCreateRequest: ApiKeyCreateRequest;
};
export type AuthApiKeyListApiResponse =
  /** status 200 The workspace's API keys */ ApiKeyListResponse;
export type AuthApiKeyListApiArg = void;
export type AuthApiKeyRevokeApiResponse = unknown;
export type AuthApiKeyRevokeApiArg = {
  id: string;
};
export type CreateWorkspaceInviteApiResponse =
  /** status 201 Created invite */ Invite;
export type CreateWorkspaceInviteApiArg = {
  id: string;
  createInviteRequest: CreateInviteRequest;
};
export type ListWorkspaceInvitesApiResponse =
  /** status 200 Pending invites */ Invite[];
export type ListWorkspaceInvitesApiArg = {
  id: string;
};
export type RevokeWorkspaceInviteApiResponse = unknown;
export type RevokeWorkspaceInviteApiArg = {
  id: string;
  inviteId: string;
};
export type ListMailboxesApiResponse =
  /** status 200 Mailboxes in the workspace */ Mailbox[];
export type ListMailboxesApiArg = void;
export type ConnectMailboxApiResponse =
  /** status 200 Connected mailbox */ Mailbox;
export type ConnectMailboxApiArg = {
  connectMailboxRequest: ConnectMailboxRequest;
};
export type GetMailboxApiResponse = /** status 200 Mailbox */ Mailbox;
export type GetMailboxApiArg = {
  id: string;
};
export type DeleteMailboxApiResponse = unknown;
export type DeleteMailboxApiArg = {
  id: string;
};
export type PauseMailboxApiResponse = /** status 200 Paused mailbox */ Mailbox;
export type PauseMailboxApiArg = {
  id: string;
};
export type ResumeMailboxApiResponse =
  /** status 200 Resumed mailbox */ Mailbox;
export type ResumeMailboxApiArg = {
  id: string;
};
export type GetMailboxWarmupApiResponse =
  /** status 200 Warmup detail for one mailbox, with a 30-day daily series */ WarmupDetail;
export type GetMailboxWarmupApiArg = {
  id: string;
};
export type EnableMailboxWarmupApiResponse =
  /** status 200 Warmup enabled/updated */ WarmupParticipant;
export type EnableMailboxWarmupApiArg = {
  id: string;
  warmupSettings: WarmupSettings;
};
export type DisableMailboxWarmupApiResponse = unknown;
export type DisableMailboxWarmupApiArg = {
  id: string;
};
export type GetWarmupOverviewApiResponse =
  /** status 200 Warmup overview */ WarmupOverview;
export type GetWarmupOverviewApiArg = void;
export type GetPulseApiResponse = /** status 200 Workspace pulse */ Pulse;
export type GetPulseApiArg = void;
export type ListListsApiResponse = /** status 200 Lists */ List[];
export type ListListsApiArg = void;
export type CreateListApiResponse = /** status 200 Created list */ List;
export type CreateListApiArg = {
  body: {
    name: string;
  };
};
export type ImportContactsApiResponse =
  /** status 200 Import result */ ImportResult;
export type ImportContactsApiArg = {
  list: string;
  body: {
    file?: Blob;
  };
};
export type ListContactsApiResponse =
  /** status 200 A page of contacts */ ContactPage;
export type ListContactsApiArg = {
  /** Restrict to one list. Omit for all contacts in the workspace. */
  list?: string;
  /** Case-insensitive substring match across email, first name, last name and company. Minimum 2 characters. */
  q?: string;
  sort?: ContactSort;
  /** Opaque cursor from a previous page's next_cursor/prev_cursor. Omit for the first page. Must match the current sort. */
  cursor?: string;
  limit?: number;
};
export type ListSendingDomainsApiResponse =
  /** status 200 One row per sending domain */ SendingDomain[];
export type ListSendingDomainsApiArg = void;
export type CheckSendingDomainApiResponse =
  /** status 200 Updated status */ SendingDomain;
export type CheckSendingDomainApiArg = {
  domain: string;
};
export type GetWorkspaceDeliverabilityApiResponse =
  /** status 200 Score, components, series and at-risk lists */ DeliverabilityReport;
export type GetWorkspaceDeliverabilityApiArg = void;
export type GetCampaignDeliverabilityApiResponse =
  /** status 200 Score for one campaign, plus any automatic pauses */ CampaignDeliverability;
export type GetCampaignDeliverabilityApiArg = {
  id: string;
};
export type UpdateCampaignGuardrailsApiResponse =
  /** status 200 Updated */ CampaignGuardrails;
export type UpdateCampaignGuardrailsApiArg = {
  id: string;
  campaignGuardrails: CampaignGuardrails;
};
export type IngestDeliverabilityEventApiResponse = unknown;
export type IngestDeliverabilityEventApiArg = {
  deliverabilityEvent: DeliverabilityEvent;
};
export type ListCampaignsApiResponse = /** status 200 Campaigns */ Campaign[];
export type ListCampaignsApiArg = void;
export type CreateCampaignApiResponse =
  /** status 200 Created campaign */ Campaign;
export type CreateCampaignApiArg = {
  createCampaignRequest: CreateCampaignRequest;
};
export type GetCampaignApiResponse =
  /** status 200 Campaign with steps + enrollment counts */ CampaignDetail;
export type GetCampaignApiArg = {
  id: string;
};
export type RenameCampaignApiResponse =
  /** status 200 Renamed campaign */ Campaign;
export type RenameCampaignApiArg = {
  id: string;
  renameCampaignRequest: RenameCampaignRequest;
};
export type DeleteCampaignApiResponse = unknown;
export type DeleteCampaignApiArg = {
  id: string;
};
export type PauseCampaignApiResponse = unknown;
export type PauseCampaignApiArg = {
  id: string;
};
export type ResumeCampaignApiResponse = unknown;
export type ResumeCampaignApiArg = {
  id: string;
};
export type UpdateCampaignTrackingApiResponse =
  /** status 200 Tracking flag updated */ {
    tracking_enabled?: boolean;
  };
export type UpdateCampaignTrackingApiArg = {
  id: string;
  updateCampaignTrackingRequest: UpdateCampaignTrackingRequest;
};
export type GetCampaignScheduleApiResponse =
  /** status 200 The campaign's timezone, weekly send windows, and a preview of upcoming send times */ CampaignSchedule;
export type GetCampaignScheduleApiArg = {
  id: string;
};
export type UpdateCampaignScheduleApiResponse =
  /** status 200 Schedule replaced */ CampaignSchedule;
export type UpdateCampaignScheduleApiArg = {
  id: string;
  campaignScheduleRequest: CampaignScheduleRequest;
};
export type GetCampaignSendersApiResponse =
  /** status 200 The campaign's sender pool and rotation mode */ CampaignSenderPool;
export type GetCampaignSendersApiArg = {
  id: string;
};
export type UpdateCampaignSendersApiResponse =
  /** status 200 Sender pool replaced */ CampaignSenderPool;
export type UpdateCampaignSendersApiArg = {
  id: string;
  campaignSenderPoolRequest: CampaignSenderPoolRequest;
};
export type ListCampaignEnrollmentsApiResponse =
  /** status 200 Per-contact reply status for the campaign's enrollments */ CampaignEnrollment[];
export type ListCampaignEnrollmentsApiArg = {
  id: string;
  limit?: number;
  offset?: number;
};
export type ListStepsApiResponse =
  /** status 200 Steps in order */ SequenceStep[];
export type ListStepsApiArg = {
  id: string;
};
export type CreateStepApiResponse =
  /** status 200 Created step (appended at end) */ SequenceStep;
export type CreateStepApiArg = {
  id: string;
  stepRequest: StepRequest;
};
export type UpdateStepApiResponse =
  /** status 200 Updated step (content edits allowed while running — live-reference) */ SequenceStep;
export type UpdateStepApiArg = {
  id: string;
  stepId: string;
  stepRequest: StepRequest;
};
export type DeleteStepApiResponse = unknown;
export type DeleteStepApiArg = {
  id: string;
  stepId: string;
};
export type ReorderStepsApiResponse =
  /** status 200 Steps in the new order */ SequenceStep[];
export type ReorderStepsApiArg = {
  id: string;
  reorderStepsRequest: ReorderStepsRequest;
};
export type LaunchCampaignApiResponse =
  /** status 200 Enrollment + queue counts */ {
    queued?: number;
    total_enrolled?: number;
    failed_enqueue_count?: number;
  };
export type LaunchCampaignApiArg = {
  id: string;
};
export type UnsubscribeConfirmPageApiResponse = unknown;
export type UnsubscribeConfirmPageApiArg = {
  token: string;
};
export type UnsubscribeApiResponse = unknown;
export type UnsubscribeApiArg = {
  token: string;
};
export type TrackOpenApiResponse = unknown;
export type TrackOpenApiArg = {
  token: string;
};
export type TrackClickApiResponse = unknown;
export type TrackClickApiArg = {
  token: string;
};
export type Oauth2RegisterApiResponse =
  /** status 201 The registered client; client_secret present only for a confidential client (once) */ OAuth2Client;
export type Oauth2RegisterApiArg = {
  oAuth2RegisterRequest: OAuth2RegisterRequest;
};
export type Oauth2AuthorizeApiResponse = unknown;
export type Oauth2AuthorizeApiArg = {
  responseType: "code";
  clientId: string;
  redirectUri: string;
  scope: string;
  state?: string;
  codeChallenge: string;
  codeChallengeMethod: "S256";
};
export type Oauth2ConsentDataApiResponse =
  /** status 200 Consent request display data */ OAuth2ConsentData;
export type Oauth2ConsentDataApiArg = {
  consentId: string;
};
export type Oauth2ConsentDecideApiResponse =
  /** status 200 The URL to navigate the browser to (client redirect_uri with code+state, or error=access_denied+state) */ OAuth2RedirectTo;
export type Oauth2ConsentDecideApiArg = {
  oAuth2ConsentDecision: OAuth2ConsentDecision;
};
export type Oauth2ListClientsApiResponse =
  /** status 200 The workspace's OAuth clients */ OAuth2ClientList;
export type Oauth2ListClientsApiArg = void;
export type Oauth2RevokeClientApiResponse = unknown;
export type Oauth2RevokeClientApiArg = {
  clientId: string;
};
export type Oauth2TokenApiResponse =
  /** status 200 The issued token pair (RFC 6749 §5.1) */ OAuth2TokenResponse;
export type Oauth2TokenApiArg = {
  oAuth2TokenRequest: OAuth2TokenRequest;
};
export type Oauth2IntrospectApiResponse =
  /** status 200 Token metadata (active) or {"active": false} */ OAuth2IntrospectResponse;
export type Oauth2IntrospectApiArg = {
  oAuth2IntrospectRequest: OAuth2IntrospectRequest;
};
export type Oauth2RevokeApiResponse = unknown;
export type Oauth2RevokeApiArg = {
  oAuth2RevokeRequest: OAuth2RevokeRequest;
};
export type Membership = {
  workspace_id: string;
  workspace_name: string;
  role: string;
};
export type SessionResponse = {
  access_token: string;
  expires_in: number;
  user_id: string;
  active_workspace_id: string;
  role: string;
  memberships: Membership[];
  /** The signed-in user's email — the SPA's identity source after a silent refresh. */
  email: string;
};
export type RegisterRequest = {
  workspace_name: string;
  email: string;
  password: string;
};
export type TwoFactorRequiredResponse = {
  /** Always true; distinguishes this from a SessionResponse. */
  two_factor_required: boolean;
  /** Single-use, short-lived challenge token to submit to /auth/2fa/verify. */
  challenge: string;
};
export type LoginRequest = {
  email: string;
  password: string;
};
export type MeResponse = {
  user_id: string;
  active_workspace_id: string;
  role: string;
  memberships: Membership[];
  /** The caller's email address. */
  email: string;
  /** Whether the caller has confirmed their email address. */
  email_verified: boolean;
};
export type SwitchWorkspaceResponse = {
  access_token: string;
  expires_in: number;
  active_workspace_id: string;
  role: string;
};
export type SwitchWorkspaceRequest = {
  workspace_id: string;
};
export type SessionInfo = {
  id: string;
  workspace_id: string;
  user_agent?: string | null;
  ip?: string | null;
  created_at: string;
  expires_at: string;
  /** Whether this is the session tied to the caller's current access token. */
  current: boolean;
};
export type SessionListResponse = {
  sessions: SessionInfo[];
};
export type RevokeOthersResponse = {
  /** Number of other sessions revoked. */
  revoked: number;
};
export type VerifyEmailRequest = {
  token: string;
};
export type ForgotPasswordRequest = {
  email: string;
};
export type ResetPasswordRequest = {
  token: string;
  new_password: string;
};
export type AcceptInviteRequest = {
  token: string;
  password?: string;
};
export type TwoFactorStatusResponse = {
  totp_enabled: boolean;
  /** Number of unused single-use recovery codes. */
  recovery_codes_remaining: number;
};
export type TwoFactorEnrollResponse = {
  /** Base32 TOTP secret for manual entry. Returned only at enrollment. */
  secret: string;
  /** otpauth:// provisioning URI to render as a QR code. */
  otpauth_uri: string;
};
export type TwoFactorCodeRequest = {
  /** A TOTP code or a recovery code. */
  code: string;
};
export type TwoFactorConfirmResponse = {
  /** Single-use recovery codes, shown exactly once. */
  recovery_codes: string[];
};
export type TwoFactorVerifyRequest = {
  /** The challenge token returned by /auth/login. */
  challenge: string;
  /** A TOTP code or a recovery code. */
  code: string;
};
export type PasskeyInfo = {
  id: string;
  label: string;
  /** Advisory transport hints (e.g. internal, hybrid). Always present; empty when none. */
  transports: string[];
  created_at: string;
  last_used_at?: string | null;
};
export type PasskeyListResponse = {
  passkeys: PasskeyInfo[];
};
export type PasskeyBeginResponse = {
  /** Opaque single-use ceremony handle. Echo it back in the matching finish call's session_id. The server holds the real challenge; this is never the challenge itself. */
  session_id: string;
  /** The WebAuthn options object (PublicKeyCredentialCreationOptions for register, PublicKeyCredentialRequestOptions for login) as defined by the WebAuthn spec — pass straight to navigator.credentials.create/get. */
  publicKey: {
    [key: string]: any;
  };
};
export type PasskeyFinishRequest = {
  /** The opaque session_id from the matching begin call. */
  session_id: string;
  /** The PublicKeyCredential JSON returned by navigator.credentials.create (attestation) or navigator.credentials.get (assertion), serialized. */
  credential: {
    [key: string]: any;
  };
  /** Optional friendly name for the passkey (registration only). */
  label?: string;
};
export type EmailOtpStartResponse = {
  /** Generic acknowledgement; identical whether or not the email matched an account. */
  status: string;
};
export type EmailOtpStartRequest = {
  email: string;
};
export type EmailOtpVerifyRequest = {
  email: string;
  /** The 6-digit numeric code delivered by email. */
  code: string;
};
export type ApiKey = {
  id: string;
  name: string;
  /** Public, non-secret token prefix (the part before the secret). */
  prefix: string;
  scopes: string[];
  ip_allowlist?: string[] | null;
  rate_limit_per_min?: number | null;
  expires_at?: string | null;
  revoked_at?: string | null;
  last_used_at?: string | null;
  created_at: string;
};
export type ApiKeyCreateResponse = {
  /** The full API key token (inrd_<prefix>_<secret>). SHOWN EXACTLY ONCE — store it now; it is never retrievable again. */
  token: string;
  api_key: ApiKey;
};
export type ApiKeyCreateRequest = {
  /** Operator-facing label for the key. */
  name: string;
  /** Granted scopes; a subset of the owned vocabulary (e.g. mailboxes:read, campaigns:send). The key holds exactly these. */
  scopes: string[];
  /** Optional IPs or CIDRs the key may be used from; a bare IP is treated as a host route. Omit or leave empty for no restriction. */
  ip_allowlist?: string[] | null;
  /** Optional per-minute request cap. Omit for unlimited. */
  rate_limit_per_min?: number | null;
  /** Optional RFC3339 expiry. Omit for a key that never expires. */
  expires_at?: string | null;
};
export type ApiKeyListResponse = {
  api_keys: ApiKey[];
};
export type Invite = {
  id?: string;
  email?: string;
  role?: string;
  status?: string;
  expires_at?: string;
  created_at?: string;
};
export type CreateInviteRequest = {
  email: string;
  role: "admin" | "member";
};
export type Mailbox = {
  id?: string;
  email?: string;
  display_name?: string;
  provider?: string;
  smtp_host?: string;
  smtp_port?: number;
  smtp_username?: string;
  imap_host?: string;
  imap_port?: number;
  imap_username?: string;
  allow_plaintext?: boolean;
  daily_cap?: number;
  min_interval_seconds?: number;
  ramp_enabled?: boolean;
  ramp_start_cap?: number;
  ramp_days?: number;
  status?: string;
  last_error?: string;
  created_at?: string;
};
export type ConnectMailboxRequest = {
  email: string;
  display_name?: string;
  smtp_host: string;
  smtp_port: number;
  smtp_username?: string;
  imap_host: string;
  imap_port: number;
  imap_username?: string;
  secret: string;
  /** Explicit opt-out from TLS, persisted on the mailbox so the connect test AND every subsequent send apply the SAME policy (rare cleartext-only internal relay). Omitted/false enforces TLS (STARTTLS on 25/587/2525, implicit TLS on 465); an absent value can never silently downgrade to cleartext auth. Replaces the removed use_tls flag. */
  allow_plaintext?: boolean;
};
export type WarmupParticipant = {
  mailbox_id: string;
  enabled: boolean;
  start_volume: number;
  max_volume: number;
  ramp_increment: number;
  reply_rate: number;
  /** reputation state derived from inbox-placement and behavior signals */
  health_state: "healthy" | "watch" | "throttled" | "paused";
  /** human-readable explanation of a non-healthy state */
  health_reason: string;
  started_at: string;
  /** warmup emails sent by this mailbox today */
  today_sent: number;
  /** today's intended (un-jittered) daily ramp target; 0 when paused. The worker applies a ±20% per-day jitter factor, so today_sent may occasionally exceed this. */
  today_target: number;
};
export type WarmupDayStat = {
  /** UTC day */
  day: string;
  /** warmup mail this mailbox sent */
  sent: number;
  /** warmup mail this mailbox received */
  received: number;
  /** of this mailbox's SENT mail, how many landed in partners' inboxes (sender placement) */
  inbox: number;
  /** of this mailbox's SENT mail, how many landed in partners' spam (sender placement) */
  spam: number;
  replies: number;
};
export type WarmupDetail = {
  participant: WarmupParticipant;
  /** daily stats, oldest first, up to 30 days */
  series: WarmupDayStat[];
};
export type WarmupSettings = {
  /** warmup emails/day at day 0 */
  start_volume?: number;
  /** daily ceiling the ramp climbs to */
  max_volume?: number;
  /** emails/day added each day */
  ramp_increment?: number;
  /** probability a warmup send is an in-thread reply */
  reply_rate?: number;
};
export type WarmupMailbox = {
  mailbox_id: string;
  email: string;
  enabled: boolean;
  health_state: "healthy" | "watch" | "throttled" | "paused";
  health_reason: string;
  today_sent: number;
  today_target: number;
  /** of this mailbox's SENT warmup mail over 7 days, the fraction that landed in partners' inboxes (0..1) — a sender-deliverability signal */
  inbox_rate_7d: number;
  /** of this mailbox's SENT warmup mail over 7 days, the fraction that landed in partners' spam (0..1) */
  spam_rate_7d: number;
};
export type WarmupOverview = {
  pool_size: number;
  active: boolean;
  mailboxes: WarmupMailbox[];
};
export type PulseAttention = {
  /** stable machine identifier; current producers: mailbox_error, senders_gated, dmarc_failing, cap_consumed */
  kind: string;
  severity: "danger" | "warn" | "info";
  count: number;
  /** human-readable explanation derived from real data */
  reason: string;
  /** console destination that shows/fixes the condition */
  href: string;
};
export type Pulse = {
  mailboxes: {
    total: number;
    active: number;
    paused: number;
    error: number;
  };
  /** enabled warmup participants bucketed by live health; at_risk = throttled + paused */
  warmup: {
    pool: number;
    healthy: number;
    watch: number;
    at_risk: number;
  };
  campaigns: {
    total: number;
    running: number;
    draft: number;
    paused: number;
  };
  contacts: {
    total: number;
  };
  sending: {
    /** cold sends completed this UTC day (same boundary the caps enforce) */
    sent_today: number;
    /** today's workspace capacity: each active mailbox's ramped cap scaled by its warmup health */
    daily_cap: number;
  };
  inbox: {
    /** always 0 until the inbox ships */
    unread: number;
    /** always 0 until the inbox ships */
    interested: number;
  };
  attention: PulseAttention[];
};
export type List = {
  id?: string;
  name?: string;
};
export type ImportResult = {
  imported?: number;
  skipped?: number;
  duplicates?: number;
};
export type Contact = {
  id?: string;
  email?: string;
  first_name?: string;
};
export type ContactPage = {
  items: Contact[];
  /** Cursor for the following page; null on the last page. */
  next_cursor?: string | null;
  /** Cursor for the preceding page; null on the first page. */
  prev_cursor?: string | null;
  /** Matching contacts. Exact when below the cap; equal to the cap when total_is_capped is true. */
  total: number;
  /** True when there are at least `total` matches and counting stopped there. Render as "N+" — counting further would be an unbounded scan. */
  total_is_capped: boolean;
};
export type ContactSort = "newest" | "oldest" | "email";
export type DomainAuthState = "unknown" | "passing" | "failing";
export type SpfStatus = {
  found: boolean;
  /** The v=spf1 record as published, when found. */
  record?: string;
};
export type DmarcStatus = {
  found: boolean;
  /** The p= tag. "none" is monitoring only, not enforcement, and should be surfaced differently from quarantine/reject. */
  policy?: "" | "none" | "quarantine" | "reject";
};
export type DkimStatus = {
  found: boolean;
  /** The selector that matched, when one did. */
  selector?: string;
};
export type SendingDomain = {
  domain: string;
  state: DomainAuthState;
  spf: SpfStatus;
  dmarc: DmarcStatus;
  dkim: DkimStatus;
  /** How many of this workspace's mailboxes send from this domain. */
  mailbox_count: number;
  /** null when never checked. */
  checked_at?: string | null;
};
export type ScoreConfidence = "low" | "medium" | "high";
export type ScoreComponent = {
  key: "bounce" | "complaint" | "spam_placement" | "warmup" | "domain_auth";
  label: string;
  /** Points subtracted from 100 by this component. */
  penalty: number;
  /** Observed rate as a percentage, when measured. */
  rate?: number | null;
  /** False when no feed exists for this signal. False means ABSENT, not clean: render it as "not measured", never as 0%. */
  measured: boolean;
  /** Human explanation, e.g. the warmup state driving the penalty. */
  detail?: string;
};
export type DeliverabilityScore = {
  value: number;
  confidence: ScoreConfidence;
  /** Sample the score was computed over. */
  delivered: number;
  components: ScoreComponent[];
};
export type DeliverabilityPoint = {
  date: string;
  delivered: number;
  bounced: number;
  complained?: number | null;
  spam_placed?: number | null;
};
export type AtRiskItem = {
  /** Mailbox address or domain. */
  label: string;
  reason: string;
};
export type DeliverabilityReport = {
  score: DeliverabilityScore;
  series: DeliverabilityPoint[];
  at_risk_mailboxes: AtRiskItem[];
  at_risk_domains: AtRiskItem[];
};
export type CampaignGuardrails = {
  auto_pause_enabled: boolean;
  bounce_pause_pct: number;
  complaint_pause_pct: number;
};
export type CampaignPauseEvent = {
  reason: "bounce_spike" | "complaint_spike";
  metric: "bounce_rate" | "complaint_rate";
  /** The observed rate that tripped it. */
  value: number;
  threshold: number;
  /** Sample it was judged on, always at or above the minimum. */
  delivered: number;
  created_at: string;
};
export type CampaignDeliverability = {
  score: DeliverabilityScore;
  guardrails: CampaignGuardrails;
  pause_events: CampaignPauseEvent[];
  /** ok; warn when a rate sits in the band below its pause threshold; paused when the breaker has fired. */
  verdict: "ok" | "warn" | "paused";
};
export type DeliverabilityEvent = {
  kind: "complaint" | "bounce";
  email: string;
  /** The provider's own id; the idempotency key. */
  provider_event_id: string;
  send_id?: string | null;
};
export type Campaign = {
  id?: string;
  name?: string;
  subject?: string;
  status?: string;
  stats?: {
    [key: string]: number;
  };
};
export type CreateCampaignRequest = {
  name: string;
  mailbox_id: string;
  list_id: string;
  subject: string;
  body_text?: string;
  body_html?: string;
};
export type SequenceStep = {
  id?: string;
  step_order?: number;
  /** wait after the previous step's send before this one */
  delay_seconds?: number;
  subject?: string;
  body_text?: string;
  body_html?: string;
};
export type Metrics = {
  sent?: number;
  opens_indicative?: number;
  clicks?: number;
  replies?: number;
  bounces?: number;
  unsubscribes?: number;
  open_rate?: number;
  click_rate?: number;
  reply_rate?: number;
  bounce_rate?: number;
  unsub_rate?: number;
};
export type CampaignDetail = {
  id?: string;
  name?: string;
  subject?: string;
  status?: string;
  /** Whether open/click tracking is injected into this campaign's sends. */
  tracking_enabled?: boolean;
  /** send counts by status */
  stats?: {
    [key: string]: number;
  };
  /** enrollment counts by status */
  enrollments?: {
    [key: string]: number;
  };
  steps?: SequenceStep[];
  metrics?: Metrics;
};
export type RenameCampaignRequest = {
  name: string;
};
export type UpdateCampaignTrackingRequest = {
  enabled?: boolean;
};
export type SendWindowInterval = {
  start_minute: number;
  end_minute: number;
};
export type SendWindowDay = {
  weekday: number;
  intervals: SendWindowInterval[];
};
export type CampaignSchedule = {
  /** IANA zone the windows are interpreted in */
  timezone: string;
  days: SendWindowDay[];
  /** Campaign-wide cap on sends per UTC day, across every mailbox in the pool. null means no campaign limit. It can only lower throughput: a mailbox is never raised above its own ramped, health-scaled daily cap. Bounded at 1000000 because the column is a 32-bit integer — an unbounded value would reach Postgres out of range and surface as a 500 instead of a validation error. */
  daily_limit?: number | null;
  /** Human-readable preview of the next few send instants this schedule produces, in its own timezone. */
  preview?: string[];
};
export type CampaignScheduleRequest = {
  timezone: string;
  days: SendWindowDay[];
  /** Campaign-wide sends per UTC day; null or omitted clears the limit. */
  daily_limit?: number | null;
};
export type RotationMode = "round_robin" | "least_recently_used" | "weighted";
export type CampaignSender = {
  mailbox_id: string;
  /** Read-only, for display */
  email: string;
  /** Read-only mailbox provider */
  provider?: string;
  /** Read-only mailbox status */
  status?: string;
  weight: number;
  enabled: boolean;
  /** Contacts assigned to this mailbox so far */
  assigned_count: number;
  last_assigned_at?: string | null;
  /** Read-only warmup health; null when the mailbox is not warming up. */
  health_state?: ("healthy" | "watch" | "throttled" | "paused" | null) | null;
  /** Read-only. False when this mailbox is not taking cold volume right now — paused by warmup, held out of rotation, or an inactive mailbox. */
  sending?: boolean;
  /** Read-only effective cap for today, after ramp and after warmup-health scaling. */
  cap_today?: number;
  /** Read-only count of sends from this mailbox today (UTC day). */
  sent_today?: number;
};
export type CampaignSenderPool = {
  rotation_mode: RotationMode;
  senders: CampaignSender[];
};
export type CampaignSenderRequest = {
  mailbox_id: string;
  weight?: number;
  enabled?: boolean;
};
export type CampaignSenderPoolRequest = {
  rotation_mode: RotationMode;
  senders: CampaignSenderRequest[];
};
export type CampaignEnrollment = {
  email: string;
  first_name: string;
  /** enrollment lifecycle status (active/completed/stopped) */
  status: string;
  /** classified sentiment/intent bucket of the reply */
  reply_class:
    | (
        | "positive"
        | "negative"
        | "neutral"
        | "auto_reply"
        | "out_of_office"
        | "unsubscribe"
        | "unknown"
      )
    | null;
  /** layer that decided the class (header/lexicon/model) */
  reply_source: string | null;
  /** RFC3339 timestamp the reply was classified */
  replied_at: string | null;
};
export type StepRequest = {
  delay_seconds?: number;
  subject?: string;
  body_text?: string;
  body_html?: string;
};
export type ReorderStepsRequest = {
  /** the FULL ordered list of the campaign's step ids, in the desired order */
  step_ids: string[];
};
export type OAuth2Client = {
  client_id: string;
  /** Present ONLY on a confidential client's registration response, shown exactly once. Never on a list. */
  client_secret?: string;
  client_name: string;
  redirect_uris: string[];
  grant_types: string[];
  response_types: string[];
  /** space-delimited registered scopes */
  scope: string;
  client_type: "public" | "confidential";
  token_endpoint_auth_method: string;
  created_at: string;
  revoked_at?: string | null;
};
export type OAuth2RegisterRequest = {
  client_name: string;
  /** https (or http loopback) URIs, no fragment; exact-matched at /authorize */
  redirect_uris: string[];
  /** defaults to [authorization_code] */
  grant_types?: ("authorization_code" | "refresh_token")[];
  /** defaults to [code] */
  response_types?: "code"[];
  /** space-delimited; each scope must be OAuth-grantable */
  scope?: string;
  /** omitted or "none" => public PKCE client (no secret); client_secret_* => confidential */
  token_endpoint_auth_method?:
    "none" | "client_secret_basic" | "client_secret_post";
};
export type OAuth2ConsentData = {
  client_name: string;
  requested_scopes: string[];
  redirect_uri: string;
};
export type OAuth2RedirectTo = {
  /** URL the SPA navigates the browser to */
  redirect_to: string;
};
export type OAuth2ConsentDecision = {
  consent_id: string;
  decision: "approve" | "deny";
};
export type OAuth2ClientList = {
  clients: OAuth2Client[];
};
export type OAuth2TokenResponse = {
  /** opaque bearer token (inoa_ prefix) */
  access_token: string;
  token_type: "Bearer";
  /** access-token lifetime in seconds */
  expires_in: number;
  /** rotating single-use refresh token (inor_ prefix) */
  refresh_token: string;
  /** space-delimited granted scopes */
  scope: string;
};
export type OAuth2TokenError = {
  error:
    | "invalid_request"
    | "invalid_client"
    | "invalid_grant"
    | "unsupported_grant_type"
    | "invalid_scope"
    | "server_error";
  error_description?: string;
};
export type OAuth2TokenRequest = {
  grant_type: "authorization_code" | "refresh_token";
  /** authorization_code grant only */
  code?: string;
  /** authorization_code grant only; must exactly match the code's redirect_uri */
  redirect_uri?: string;
  /** authorization_code grant only; the PKCE verifier (S256) */
  code_verifier?: string;
  /** refresh_token grant only */
  refresh_token?: string;
  /** refresh_token grant only; a space-delimited SUBSET of the token's scopes (narrowing) */
  scope?: string;
  client_id?: string;
  /** confidential client, client_secret_post (or use HTTP Basic) */
  client_secret?: string;
};
export type OAuth2IntrospectResponse = {
  active: boolean;
  scope?: string;
  client_id?: string;
  /** the resource owner (user) id */
  sub?: string;
  /** expiry as a Unix timestamp */
  exp?: number;
  token_type?: string;
};
export type OAuth2IntrospectRequest = {
  token: string;
  token_type_hint?: "access_token" | "refresh_token";
  client_id?: string;
  client_secret?: string;
};
export type OAuth2RevokeRequest = {
  token: string;
  token_type_hint?: "access_token" | "refresh_token";
  client_id?: string;
  client_secret?: string;
};
export const {
  useAuthRegisterMutation,
  useAuthLoginMutation,
  useAuthRefreshMutation,
  useAuthLogoutMutation,
  useAuthMeQuery,
  useAuthLogoutAllMutation,
  useAuthSwitchWorkspaceMutation,
  useAuthListSessionsQuery,
  useAuthRevokeOtherSessionsMutation,
  useAuthRevokeSessionMutation,
  useAuthVerifyEmailMutation,
  useAuthResendVerificationMutation,
  useAuthForgotPasswordMutation,
  useAuthResetPasswordMutation,
  useAuthAcceptInviteMutation,
  useAuthTwoFactorStatusQuery,
  useAuthTwoFactorEnrollMutation,
  useAuthTwoFactorDisableMutation,
  useAuthTwoFactorConfirmMutation,
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
  useCreateWorkspaceInviteMutation,
  useListWorkspaceInvitesQuery,
  useRevokeWorkspaceInviteMutation,
  useListMailboxesQuery,
  useConnectMailboxMutation,
  useGetMailboxQuery,
  useDeleteMailboxMutation,
  usePauseMailboxMutation,
  useResumeMailboxMutation,
  useGetMailboxWarmupQuery,
  useEnableMailboxWarmupMutation,
  useDisableMailboxWarmupMutation,
  useGetWarmupOverviewQuery,
  useGetPulseQuery,
  useListListsQuery,
  useCreateListMutation,
  useImportContactsMutation,
  useListContactsQuery,
  useListSendingDomainsQuery,
  useCheckSendingDomainMutation,
  useGetWorkspaceDeliverabilityQuery,
  useGetCampaignDeliverabilityQuery,
  useUpdateCampaignGuardrailsMutation,
  useIngestDeliverabilityEventMutation,
  useListCampaignsQuery,
  useCreateCampaignMutation,
  useGetCampaignQuery,
  useRenameCampaignMutation,
  useDeleteCampaignMutation,
  usePauseCampaignMutation,
  useResumeCampaignMutation,
  useUpdateCampaignTrackingMutation,
  useGetCampaignScheduleQuery,
  useUpdateCampaignScheduleMutation,
  useGetCampaignSendersQuery,
  useUpdateCampaignSendersMutation,
  useListCampaignEnrollmentsQuery,
  useListStepsQuery,
  useCreateStepMutation,
  useUpdateStepMutation,
  useDeleteStepMutation,
  useReorderStepsMutation,
  useLaunchCampaignMutation,
  useUnsubscribeConfirmPageQuery,
  useUnsubscribeMutation,
  useTrackOpenQuery,
  useTrackClickQuery,
  useOauth2RegisterMutation,
  useOauth2AuthorizeQuery,
  useOauth2ConsentDataQuery,
  useOauth2ConsentDecideMutation,
  useOauth2ListClientsQuery,
  useOauth2RevokeClientMutation,
  useOauth2TokenMutation,
  useOauth2IntrospectMutation,
  useOauth2RevokeMutation,
} = injectedRtkApi;
