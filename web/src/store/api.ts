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
    authGoogleSignInRedirect: build.query<
      AuthGoogleSignInRedirectApiResponse,
      AuthGoogleSignInRedirectApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/oauth/google/start`,
        params: {
          return_to: queryArg.returnTo,
        },
      }),
    }),
    authGoogleSignInStart: build.mutation<
      AuthGoogleSignInStartApiResponse,
      AuthGoogleSignInStartApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/oauth/google/start`,
        method: "POST",
        body: queryArg.googleSignInStartRequest,
      }),
    }),
    authGoogleSignInCallback: build.query<
      AuthGoogleSignInCallbackApiResponse,
      AuthGoogleSignInCallbackApiArg
    >({
      query: (queryArg) => ({
        url: `/auth/oauth/google/callback`,
        params: {
          code: queryArg.code,
          state: queryArg.state,
          error: queryArg.error,
        },
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
    completeWorkspaceOnboarding: build.mutation<
      CompleteWorkspaceOnboardingApiResponse,
      CompleteWorkspaceOnboardingApiArg
    >({
      query: (queryArg) => ({
        url: `/workspaces/${queryArg.id}/onboarding/complete`,
        method: "POST",
        body: queryArg.completeOnboardingRequest,
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
    getAiSettings: build.query<GetAiSettingsApiResponse, GetAiSettingsApiArg>({
      query: () => ({ url: `/ai/settings` }),
    }),
    updateAiSettings: build.mutation<
      UpdateAiSettingsApiResponse,
      UpdateAiSettingsApiArg
    >({
      query: (queryArg) => ({
        url: `/ai/settings`,
        method: "PUT",
        body: queryArg.aiSettingsUpdate,
      }),
    }),
    listAiProviders: build.query<
      ListAiProvidersApiResponse,
      ListAiProvidersApiArg
    >({
      query: () => ({ url: `/ai/providers` }),
    }),
    createAiProvider: build.mutation<
      CreateAiProviderApiResponse,
      CreateAiProviderApiArg
    >({
      query: (queryArg) => ({
        url: `/ai/providers`,
        method: "POST",
        body: queryArg.aiProviderCreateRequest,
      }),
    }),
    updateAiProvider: build.mutation<
      UpdateAiProviderApiResponse,
      UpdateAiProviderApiArg
    >({
      query: (queryArg) => ({
        url: `/ai/providers/${queryArg.id}`,
        method: "PUT",
        body: queryArg.aiProviderUpdateRequest,
      }),
    }),
    deleteAiProvider: build.mutation<
      DeleteAiProviderApiResponse,
      DeleteAiProviderApiArg
    >({
      query: (queryArg) => ({
        url: `/ai/providers/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    discoverAiProviderModels: build.mutation<
      DiscoverAiProviderModelsApiResponse,
      DiscoverAiProviderModelsApiArg
    >({
      query: (queryArg) => ({
        url: `/ai/providers/${queryArg.id}/discover`,
        method: "POST",
      }),
    }),
    listAiModels: build.query<ListAiModelsApiResponse, ListAiModelsApiArg>({
      query: () => ({ url: `/ai/models` }),
    }),
    createAiModel: build.mutation<
      CreateAiModelApiResponse,
      CreateAiModelApiArg
    >({
      query: (queryArg) => ({
        url: `/ai/models`,
        method: "POST",
        body: queryArg.aiModelCreateRequest,
      }),
    }),
    deleteAiModel: build.mutation<
      DeleteAiModelApiResponse,
      DeleteAiModelApiArg
    >({
      query: (queryArg) => ({
        url: `/ai/models/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    listAgentThreads: build.query<
      ListAgentThreadsApiResponse,
      ListAgentThreadsApiArg
    >({
      query: (queryArg) => ({
        url: `/agent/threads`,
        params: {
          offset: queryArg.offset,
          limit: queryArg.limit,
        },
      }),
    }),
    createAgentThread: build.mutation<
      CreateAgentThreadApiResponse,
      CreateAgentThreadApiArg
    >({
      query: () => ({ url: `/agent/threads`, method: "POST" }),
    }),
    getAgentThread: build.query<
      GetAgentThreadApiResponse,
      GetAgentThreadApiArg
    >({
      query: (queryArg) => ({ url: `/agent/threads/${queryArg.id}` }),
    }),
    renameAgentThread: build.mutation<
      RenameAgentThreadApiResponse,
      RenameAgentThreadApiArg
    >({
      query: (queryArg) => ({
        url: `/agent/threads/${queryArg.id}`,
        method: "PATCH",
        body: queryArg.body,
      }),
    }),
    deleteAgentThread: build.mutation<
      DeleteAgentThreadApiResponse,
      DeleteAgentThreadApiArg
    >({
      query: (queryArg) => ({
        url: `/agent/threads/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    sendAgentMessage: build.mutation<
      SendAgentMessageApiResponse,
      SendAgentMessageApiArg
    >({
      query: (queryArg) => ({
        url: `/agent/threads/${queryArg.id}/messages`,
        method: "POST",
        body: queryArg.agentSendRequest,
      }),
    }),
    listAgentQueue: build.query<
      ListAgentQueueApiResponse,
      ListAgentQueueApiArg
    >({
      query: (queryArg) => ({ url: `/agent/threads/${queryArg.id}/queue` }),
    }),
    deleteAgentQueuedMessage: build.mutation<
      DeleteAgentQueuedMessageApiResponse,
      DeleteAgentQueuedMessageApiArg
    >({
      query: (queryArg) => ({
        url: `/agent/threads/${queryArg.id}/queue/${queryArg.messageId}`,
        method: "DELETE",
      }),
    }),
    stopAgentRun: build.mutation<StopAgentRunApiResponse, StopAgentRunApiArg>({
      query: (queryArg) => ({
        url: `/agent/threads/${queryArg.id}/stop`,
        method: "POST",
      }),
    }),
    streamAgentThread: build.query<
      StreamAgentThreadApiResponse,
      StreamAgentThreadApiArg
    >({
      query: (queryArg) => ({
        url: `/agent/threads/${queryArg.id}/stream`,
        headers: {
          "Last-Event-ID": queryArg["Last-Event-ID"],
        },
        params: {
          after_seq: queryArg.afterSeq,
        },
      }),
    }),
    listAgentApprovals: build.query<
      ListAgentApprovalsApiResponse,
      ListAgentApprovalsApiArg
    >({
      query: (queryArg) => ({
        url: `/agent/approvals`,
        params: {
          status: queryArg.status,
          limit: queryArg.limit,
        },
      }),
    }),
    getAgentApproval: build.query<
      GetAgentApprovalApiResponse,
      GetAgentApprovalApiArg
    >({
      query: (queryArg) => ({ url: `/agent/approvals/${queryArg.actionId}` }),
    }),
    decideAgentApproval: build.mutation<
      DecideAgentApprovalApiResponse,
      DecideAgentApprovalApiArg
    >({
      query: (queryArg) => ({
        url: `/agent/approvals/${queryArg.actionId}/decision`,
        method: "POST",
        body: queryArg.agentApprovalDecisionRequest,
      }),
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
    renameList: build.mutation<RenameListApiResponse, RenameListApiArg>({
      query: (queryArg) => ({
        url: `/lists/${queryArg.id}`,
        method: "PATCH",
        body: queryArg.body,
      }),
    }),
    deleteList: build.mutation<DeleteListApiResponse, DeleteListApiArg>({
      query: (queryArg) => ({ url: `/lists/${queryArg.id}`, method: "DELETE" }),
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
    listCustomFields: build.query<
      ListCustomFieldsApiResponse,
      ListCustomFieldsApiArg
    >({
      query: () => ({ url: `/custom-fields` }),
    }),
    createCustomField: build.mutation<
      CreateCustomFieldApiResponse,
      CreateCustomFieldApiArg
    >({
      query: (queryArg) => ({
        url: `/custom-fields`,
        method: "POST",
        body: queryArg.customFieldCreate,
      }),
    }),
    updateCustomField: build.mutation<
      UpdateCustomFieldApiResponse,
      UpdateCustomFieldApiArg
    >({
      query: (queryArg) => ({
        url: `/custom-fields/${queryArg.fieldId}`,
        method: "PATCH",
        body: queryArg.customFieldUpdate,
      }),
    }),
    archiveCustomField: build.mutation<
      ArchiveCustomFieldApiResponse,
      ArchiveCustomFieldApiArg
    >({
      query: (queryArg) => ({
        url: `/custom-fields/${queryArg.fieldId}`,
        method: "DELETE",
      }),
    }),
    getContactCustomFields: build.query<
      GetContactCustomFieldsApiResponse,
      GetContactCustomFieldsApiArg
    >({
      query: (queryArg) => ({ url: `/contacts/${queryArg.id}/fields` }),
    }),
    setContactCustomFields: build.mutation<
      SetContactCustomFieldsApiResponse,
      SetContactCustomFieldsApiArg
    >({
      query: (queryArg) => ({
        url: `/contacts/${queryArg.id}/fields`,
        method: "PUT",
        body: queryArg.customFieldValueSet,
      }),
    }),
    getContact: build.query<GetContactApiResponse, GetContactApiArg>({
      query: (queryArg) => ({ url: `/contacts/${queryArg.id}` }),
    }),
    setContactCompany: build.mutation<
      SetContactCompanyApiResponse,
      SetContactCompanyApiArg
    >({
      query: (queryArg) => ({
        url: `/contacts/${queryArg.id}/company`,
        method: "PUT",
        body: queryArg.contactCompanyLink,
      }),
    }),
    getContactEngagement: build.query<
      GetContactEngagementApiResponse,
      GetContactEngagementApiArg
    >({
      query: (queryArg) => ({ url: `/contacts/${queryArg.id}/engagement` }),
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
    getCampaignResults: build.query<
      GetCampaignResultsApiResponse,
      GetCampaignResultsApiArg
    >({
      query: (queryArg) => ({ url: `/campaigns/${queryArg.id}/results` }),
    }),
    exportCampaignResults: build.query<
      ExportCampaignResultsApiResponse,
      ExportCampaignResultsApiArg
    >({
      query: (queryArg) => ({ url: `/campaigns/${queryArg.id}/results.csv` }),
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
    listStepVariants: build.query<
      ListStepVariantsApiResponse,
      ListStepVariantsApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/steps/${queryArg.stepId}/variants`,
      }),
    }),
    createStepVariant: build.mutation<
      CreateStepVariantApiResponse,
      CreateStepVariantApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/steps/${queryArg.stepId}/variants`,
        method: "POST",
        body: queryArg.stepVariantRequest,
      }),
    }),
    updateStepVariant: build.mutation<
      UpdateStepVariantApiResponse,
      UpdateStepVariantApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/steps/${queryArg.stepId}/variants/${queryArg.variantId}`,
        method: "PUT",
        body: queryArg.stepVariantRequest,
      }),
    }),
    deleteStepVariant: build.mutation<
      DeleteStepVariantApiResponse,
      DeleteStepVariantApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/steps/${queryArg.stepId}/variants/${queryArg.variantId}`,
        method: "DELETE",
      }),
    }),
    setStepBaseWeight: build.mutation<
      SetStepBaseWeightApiResponse,
      SetStepBaseWeightApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/steps/${queryArg.stepId}/base-weight`,
        method: "PUT",
        body: queryArg.stepBaseWeightRequest,
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
    getCampaignPreflight: build.query<
      GetCampaignPreflightApiResponse,
      GetCampaignPreflightApiArg
    >({
      query: (queryArg) => ({ url: `/campaigns/${queryArg.id}/preflight` }),
    }),
    testSendCampaign: build.mutation<
      TestSendCampaignApiResponse,
      TestSendCampaignApiArg
    >({
      query: (queryArg) => ({
        url: `/campaigns/${queryArg.id}/test-send`,
        method: "POST",
        body: queryArg.testSendRequest,
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
    crmListCompanies: build.query<
      CrmListCompaniesApiResponse,
      CrmListCompaniesApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/companies`,
        params: {
          limit: queryArg.limit,
          cursor: queryArg.cursor,
        },
      }),
    }),
    crmCreateCompany: build.mutation<
      CrmCreateCompanyApiResponse,
      CrmCreateCompanyApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/companies`,
        method: "POST",
        body: queryArg.crmCompanyInput,
      }),
    }),
    crmGetCompany: build.query<CrmGetCompanyApiResponse, CrmGetCompanyApiArg>({
      query: (queryArg) => ({ url: `/crm/companies/${queryArg.id}` }),
    }),
    crmUpdateCompany: build.mutation<
      CrmUpdateCompanyApiResponse,
      CrmUpdateCompanyApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/companies/${queryArg.id}`,
        method: "PUT",
        body: queryArg.crmCompanyInput,
      }),
    }),
    crmDeleteCompany: build.mutation<
      CrmDeleteCompanyApiResponse,
      CrmDeleteCompanyApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/companies/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    crmListCompanyContacts: build.query<
      CrmListCompanyContactsApiResponse,
      CrmListCompanyContactsApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/companies/${queryArg.id}/contacts`,
        params: {
          limit: queryArg.limit,
          cursor: queryArg.cursor,
        },
      }),
    }),
    crmListCompanyDeals: build.query<
      CrmListCompanyDealsApiResponse,
      CrmListCompanyDealsApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/companies/${queryArg.id}/deals`,
        params: {
          limit: queryArg.limit,
          cursor: queryArg.cursor,
        },
      }),
    }),
    crmListPipelines: build.query<
      CrmListPipelinesApiResponse,
      CrmListPipelinesApiArg
    >({
      query: () => ({ url: `/crm/pipelines` }),
    }),
    crmCreatePipeline: build.mutation<
      CrmCreatePipelineApiResponse,
      CrmCreatePipelineApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/pipelines`,
        method: "POST",
        body: queryArg.crmPipelineInput,
      }),
    }),
    crmGetPipeline: build.query<
      CrmGetPipelineApiResponse,
      CrmGetPipelineApiArg
    >({
      query: (queryArg) => ({ url: `/crm/pipelines/${queryArg.id}` }),
    }),
    crmUpdatePipeline: build.mutation<
      CrmUpdatePipelineApiResponse,
      CrmUpdatePipelineApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/pipelines/${queryArg.id}`,
        method: "PUT",
        body: queryArg.crmPipelineInput,
      }),
    }),
    crmDeletePipeline: build.mutation<
      CrmDeletePipelineApiResponse,
      CrmDeletePipelineApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/pipelines/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    crmCreateStage: build.mutation<
      CrmCreateStageApiResponse,
      CrmCreateStageApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/pipelines/${queryArg.id}/stages`,
        method: "POST",
        body: queryArg.crmStageInput,
      }),
    }),
    crmUpdateStage: build.mutation<
      CrmUpdateStageApiResponse,
      CrmUpdateStageApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/pipelines/${queryArg.id}/stages/${queryArg.stageId}`,
        method: "PUT",
        body: queryArg.crmStageInput,
      }),
    }),
    crmDeleteStage: build.mutation<
      CrmDeleteStageApiResponse,
      CrmDeleteStageApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/pipelines/${queryArg.id}/stages/${queryArg.stageId}`,
        method: "DELETE",
      }),
    }),
    crmListDeals: build.query<CrmListDealsApiResponse, CrmListDealsApiArg>({
      query: (queryArg) => ({
        url: `/crm/deals`,
        params: {
          limit: queryArg.limit,
          cursor: queryArg.cursor,
        },
      }),
    }),
    crmCreateDeal: build.mutation<
      CrmCreateDealApiResponse,
      CrmCreateDealApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/deals`,
        method: "POST",
        body: queryArg.crmDealInput,
      }),
    }),
    crmGetDeal: build.query<CrmGetDealApiResponse, CrmGetDealApiArg>({
      query: (queryArg) => ({ url: `/crm/deals/${queryArg.id}` }),
    }),
    crmUpdateDeal: build.mutation<
      CrmUpdateDealApiResponse,
      CrmUpdateDealApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/deals/${queryArg.id}`,
        method: "PUT",
        body: queryArg.crmDealInput,
      }),
    }),
    crmDeleteDeal: build.mutation<
      CrmDeleteDealApiResponse,
      CrmDeleteDealApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/deals/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    crmGetBoard: build.query<CrmGetBoardApiResponse, CrmGetBoardApiArg>({
      query: (queryArg) => ({
        url: `/crm/board`,
        params: {
          pipeline_id: queryArg.pipelineId,
        },
      }),
    }),
    crmMoveDeal: build.mutation<CrmMoveDealApiResponse, CrmMoveDealApiArg>({
      query: (queryArg) => ({
        url: `/crm/deals/${queryArg.id}/move`,
        method: "POST",
        body: queryArg.crmMoveDealInput,
      }),
    }),
    crmListDealThreads: build.query<
      CrmListDealThreadsApiResponse,
      CrmListDealThreadsApiArg
    >({
      query: (queryArg) => ({ url: `/crm/deals/${queryArg.id}/threads` }),
    }),
    crmListEvents: build.query<CrmListEventsApiResponse, CrmListEventsApiArg>({
      query: (queryArg) => ({
        url: `/crm/events`,
        params: {
          target_type: queryArg.targetType,
          target_id: queryArg.targetId,
        },
      }),
    }),
    crmGetSettings: build.query<
      CrmGetSettingsApiResponse,
      CrmGetSettingsApiArg
    >({
      query: () => ({ url: `/crm/settings` }),
    }),
    crmUpdateSettings: build.mutation<
      CrmUpdateSettingsApiResponse,
      CrmUpdateSettingsApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/settings`,
        method: "PUT",
        body: queryArg.crmSettingsInput,
      }),
    }),
    crmListNotes: build.query<CrmListNotesApiResponse, CrmListNotesApiArg>({
      query: (queryArg) => ({
        url: `/crm/notes`,
        params: {
          target_type: queryArg.targetType,
          target_id: queryArg.targetId,
          limit: queryArg.limit,
          cursor: queryArg.cursor,
        },
      }),
    }),
    crmCreateNote: build.mutation<
      CrmCreateNoteApiResponse,
      CrmCreateNoteApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/notes`,
        method: "POST",
        body: queryArg.crmNoteInput,
      }),
    }),
    crmUpdateNote: build.mutation<
      CrmUpdateNoteApiResponse,
      CrmUpdateNoteApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/notes/${queryArg.id}`,
        method: "PUT",
        body: queryArg.crmNoteUpdate,
      }),
    }),
    crmDeleteNote: build.mutation<
      CrmDeleteNoteApiResponse,
      CrmDeleteNoteApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/notes/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    crmListTasks: build.query<CrmListTasksApiResponse, CrmListTasksApiArg>({
      query: (queryArg) => ({
        url: `/crm/tasks`,
        params: {
          target_type: queryArg.targetType,
          target_id: queryArg.targetId,
          limit: queryArg.limit,
          cursor: queryArg.cursor,
        },
      }),
    }),
    crmCreateTask: build.mutation<
      CrmCreateTaskApiResponse,
      CrmCreateTaskApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/tasks`,
        method: "POST",
        body: queryArg.crmTaskInput,
      }),
    }),
    crmUpdateTask: build.mutation<
      CrmUpdateTaskApiResponse,
      CrmUpdateTaskApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/tasks/${queryArg.id}`,
        method: "PUT",
        body: queryArg.crmTaskInput,
      }),
    }),
    crmDeleteTask: build.mutation<
      CrmDeleteTaskApiResponse,
      CrmDeleteTaskApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/tasks/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    crmListContactEmails: build.query<
      CrmListContactEmailsApiResponse,
      CrmListContactEmailsApiArg
    >({
      query: (queryArg) => ({ url: `/crm/contacts/${queryArg.id}/emails` }),
    }),
    crmAddContactEmail: build.mutation<
      CrmAddContactEmailApiResponse,
      CrmAddContactEmailApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/contacts/${queryArg.id}/emails`,
        method: "POST",
        body: queryArg.crmContactEmailInput,
      }),
    }),
    crmSetPrimaryContactEmail: build.mutation<
      CrmSetPrimaryContactEmailApiResponse,
      CrmSetPrimaryContactEmailApiArg
    >({
      query: (queryArg) => ({
        url: `/crm/contacts/${queryArg.id}/emails/${queryArg.emailId}/primary`,
        method: "PUT",
      }),
    }),
    listReplyLabels: build.query<
      ListReplyLabelsApiResponse,
      ListReplyLabelsApiArg
    >({
      query: () => ({ url: `/reply-labels` }),
    }),
    createReplyLabel: build.mutation<
      CreateReplyLabelApiResponse,
      CreateReplyLabelApiArg
    >({
      query: (queryArg) => ({
        url: `/reply-labels`,
        method: "POST",
        body: queryArg.replyLabelInput,
      }),
    }),
    reorderReplyLabels: build.mutation<
      ReorderReplyLabelsApiResponse,
      ReorderReplyLabelsApiArg
    >({
      query: (queryArg) => ({
        url: `/reply-labels/reorder`,
        method: "PUT",
        body: queryArg.replyLabelReorderInput,
      }),
    }),
    updateReplyLabel: build.mutation<
      UpdateReplyLabelApiResponse,
      UpdateReplyLabelApiArg
    >({
      query: (queryArg) => ({
        url: `/reply-labels/${queryArg.id}`,
        method: "PUT",
        body: queryArg.replyLabelInput,
      }),
    }),
    deleteReplyLabel: build.mutation<
      DeleteReplyLabelApiResponse,
      DeleteReplyLabelApiArg
    >({
      query: (queryArg) => ({
        url: `/reply-labels/${queryArg.id}`,
        method: "DELETE",
      }),
    }),
    listInboxThreads: build.query<
      ListInboxThreadsApiResponse,
      ListInboxThreadsApiArg
    >({
      query: (queryArg) => ({
        url: `/inbox/threads`,
        params: {
          mailbox_id: queryArg.mailboxId,
          reply_class: queryArg.replyClass,
          q: queryArg.q,
          before_last_message_at: queryArg.beforeLastMessageAt,
          before_id: queryArg.beforeId,
          limit: queryArg.limit,
        },
      }),
    }),
    getInboxThread: build.query<
      GetInboxThreadApiResponse,
      GetInboxThreadApiArg
    >({
      query: (queryArg) => ({ url: `/inbox/threads/${queryArg.id}` }),
    }),
    sendInboxReply: build.mutation<
      SendInboxReplyApiResponse,
      SendInboxReplyApiArg
    >({
      query: (queryArg) => ({
        url: `/inbox/threads/${queryArg.id}/reply`,
        method: "POST",
        body: queryArg.sendInboxReplyRequest,
      }),
    }),
    draftInboxReply: build.mutation<
      DraftInboxReplyApiResponse,
      DraftInboxReplyApiArg
    >({
      query: (queryArg) => ({
        url: `/inbox/threads/${queryArg.id}/draft-reply`,
        method: "POST",
      }),
    }),
    setInboxThreadRead: build.mutation<
      SetInboxThreadReadApiResponse,
      SetInboxThreadReadApiArg
    >({
      query: (queryArg) => ({
        url: `/inbox/threads/${queryArg.id}/read`,
        method: "PUT",
        body: queryArg.setInboxThreadReadRequest,
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
export type AuthGoogleSignInRedirectApiResponse = unknown;
export type AuthGoogleSignInRedirectApiArg = {
  /** In-app path to land on after sign-in. Validated server-side as a same-origin path (anything else is silently dropped) and remembered against the state nonce rather than echoed through Google. */
  returnTo?: string;
};
export type AuthGoogleSignInStartApiResponse =
  /** status 200 Google consent URL */ GoogleSignInStartResponse;
export type AuthGoogleSignInStartApiArg = {
  googleSignInStartRequest: GoogleSignInStartRequest;
};
export type AuthGoogleSignInCallbackApiResponse = unknown;
export type AuthGoogleSignInCallbackApiArg = {
  code?: string;
  state?: string;
  error?: string;
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
export type CompleteWorkspaceOnboardingApiResponse =
  /** status 200 Onboarding state after the call */ OnboardingResponse;
export type CompleteWorkspaceOnboardingApiArg = {
  id: string;
  completeOnboardingRequest: CompleteOnboardingRequest;
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
export type GetAiSettingsApiResponse = /** status 200 AI settings */ AiSettings;
export type GetAiSettingsApiArg = void;
export type UpdateAiSettingsApiResponse =
  /** status 200 Updated AI settings */ AiSettings;
export type UpdateAiSettingsApiArg = {
  aiSettingsUpdate: AiSettingsUpdate;
};
export type ListAiProvidersApiResponse =
  /** status 200 Configured providers */ AiProviderList;
export type ListAiProvidersApiArg = void;
export type CreateAiProviderApiResponse =
  /** status 201 Provider configured (masked) */ AiProvider;
export type CreateAiProviderApiArg = {
  aiProviderCreateRequest: AiProviderCreateRequest;
};
export type UpdateAiProviderApiResponse =
  /** status 200 Updated provider (masked) */ AiProvider;
export type UpdateAiProviderApiArg = {
  id: string;
  aiProviderUpdateRequest: AiProviderUpdateRequest;
};
export type DeleteAiProviderApiResponse = unknown;
export type DeleteAiProviderApiArg = {
  id: string;
};
export type DiscoverAiProviderModelsApiResponse =
  /** status 200 Discovery result */ AiDiscoveryResult;
export type DiscoverAiProviderModelsApiArg = {
  id: string;
};
export type ListAiModelsApiResponse =
  /** status 200 Merged model list */ AiModelList;
export type ListAiModelsApiArg = void;
export type CreateAiModelApiResponse = /** status 201 Created model */ AiModel;
export type CreateAiModelApiArg = {
  aiModelCreateRequest: AiModelCreateRequest;
};
export type DeleteAiModelApiResponse = unknown;
export type DeleteAiModelApiArg = {
  id: string;
};
export type ListAgentThreadsApiResponse =
  /** status 200 Owner-scoped threads */ AgentThreadList;
export type ListAgentThreadsApiArg = {
  offset?: number;
  limit?: number;
};
export type CreateAgentThreadApiResponse =
  /** status 201 Empty thread */ AgentThread;
export type CreateAgentThreadApiArg = void;
export type GetAgentThreadApiResponse =
  /** status 200 Thread and transcript */ AgentThread;
export type GetAgentThreadApiArg = {
  id: string;
};
export type RenameAgentThreadApiResponse =
  /** status 200 Renamed thread */ AgentThread;
export type RenameAgentThreadApiArg = {
  id: string;
  body: {
    title: string;
  };
};
export type DeleteAgentThreadApiResponse = unknown;
export type DeleteAgentThreadApiArg = {
  id: string;
};
export type SendAgentMessageApiResponse =
  /** status 202 Run started or message queued */ AgentSendResult;
export type SendAgentMessageApiArg = {
  id: string;
  agentSendRequest: AgentSendRequest;
};
export type ListAgentQueueApiResponse =
  /** status 200 Pending messages */ AgentQueue;
export type ListAgentQueueApiArg = {
  id: string;
};
export type DeleteAgentQueuedMessageApiResponse = unknown;
export type DeleteAgentQueuedMessageApiArg = {
  id: string;
  messageId: string;
};
export type StopAgentRunApiResponse = unknown;
export type StopAgentRunApiArg = {
  id: string;
};
export type StreamAgentThreadApiResponse = unknown;
export type StreamAgentThreadApiArg = {
  id: string;
  afterSeq?: number;
  "Last-Event-ID"?: string;
};
export type ListAgentApprovalsApiResponse =
  /** status 200 Owner-scoped approval actions */ AgentApprovalList;
export type ListAgentApprovalsApiArg = {
  status?: AgentApprovalStatus;
  limit?: number;
};
export type GetAgentApprovalApiResponse =
  /** status 200 Approval action */ AgentApproval;
export type GetAgentApprovalApiArg = {
  actionId: string;
};
export type DecideAgentApprovalApiResponse =
  /** status 200 Updated approval action */ AgentApproval;
export type DecideAgentApprovalApiArg = {
  actionId: string;
  agentApprovalDecisionRequest: AgentApprovalDecisionRequest;
};
export type ListListsApiResponse = /** status 200 Lists */ List[];
export type ListListsApiArg = void;
export type CreateListApiResponse = /** status 200 Created list */ List;
export type CreateListApiArg = {
  body: {
    name: string;
  };
};
export type RenameListApiResponse = /** status 200 Renamed list */ List;
export type RenameListApiArg = {
  id: string;
  body: {
    name: string;
  };
};
export type DeleteListApiResponse = unknown;
export type DeleteListApiArg = {
  id: string;
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
export type ListCustomFieldsApiResponse =
  /** status 200 The workspace's custom field definitions */ CustomFieldDef[];
export type ListCustomFieldsApiArg = void;
export type CreateCustomFieldApiResponse =
  /** status 201 The created definition */ CustomFieldDef;
export type CreateCustomFieldApiArg = {
  customFieldCreate: CustomFieldCreate;
};
export type UpdateCustomFieldApiResponse =
  /** status 200 The updated definition */ CustomFieldDef;
export type UpdateCustomFieldApiArg = {
  fieldId: string;
  customFieldUpdate: CustomFieldUpdate;
};
export type ArchiveCustomFieldApiResponse =
  /** status 200 The archived definition */ CustomFieldDef;
export type ArchiveCustomFieldApiArg = {
  fieldId: string;
};
export type GetContactCustomFieldsApiResponse =
  /** status 200 The contact's custom field values */ CustomFieldValue[];
export type GetContactCustomFieldsApiArg = {
  id: string;
};
export type SetContactCustomFieldsApiResponse =
  /** status 200 The contact's custom field values after the write */ CustomFieldValue[];
export type SetContactCustomFieldsApiArg = {
  id: string;
  customFieldValueSet: CustomFieldValueSet;
};
export type GetContactApiResponse =
  /** status 200 The contact record */ ContactDetail;
export type GetContactApiArg = {
  id: string;
};
export type SetContactCompanyApiResponse =
  /** status 200 The updated contact record */ ContactDetail;
export type SetContactCompanyApiArg = {
  id: string;
  contactCompanyLink: ContactCompanyLink;
};
export type GetContactEngagementApiResponse =
  /** status 200 The contact's engagement rollup */ ContactEngagement;
export type GetContactEngagementApiArg = {
  id: string;
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
export type GetCampaignResultsApiResponse =
  /** status 200 The campaign's per-step results */ CampaignResults;
export type GetCampaignResultsApiArg = {
  id: string;
};
export type ExportCampaignResultsApiResponse = unknown;
export type ExportCampaignResultsApiArg = {
  id: string;
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
export type ListStepVariantsApiResponse =
  /** status 200 The step's variants */ StepVariant[];
export type ListStepVariantsApiArg = {
  id: string;
  stepId: string;
};
export type CreateStepVariantApiResponse =
  /** status 201 The created variant */ StepVariant;
export type CreateStepVariantApiArg = {
  id: string;
  stepId: string;
  stepVariantRequest: StepVariantRequest;
};
export type UpdateStepVariantApiResponse =
  /** status 200 The updated variant */ StepVariant;
export type UpdateStepVariantApiArg = {
  id: string;
  stepId: string;
  variantId: string;
  stepVariantRequest: StepVariantRequest;
};
export type DeleteStepVariantApiResponse = unknown;
export type DeleteStepVariantApiArg = {
  id: string;
  stepId: string;
  variantId: string;
};
export type SetStepBaseWeightApiResponse =
  /** status 200 The step's variants after the change */ StepVariant[];
export type SetStepBaseWeightApiArg = {
  id: string;
  stepId: string;
  stepBaseWeightRequest: StepBaseWeightRequest;
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
export type GetCampaignPreflightApiResponse =
  /** status 200 Readiness report */ CampaignPreflight;
export type GetCampaignPreflightApiArg = {
  id: string;
};
export type TestSendCampaignApiResponse =
  /** status 202 Queued */ TestSendResponse;
export type TestSendCampaignApiArg = {
  id: string;
  testSendRequest: TestSendRequest;
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
export type CrmListCompaniesApiResponse =
  /** status 200 Workspace companies */ CrmCompanyList;
export type CrmListCompaniesApiArg = {
  /** Page size. Defaults to 50, capped at 200. */
  limit?: number;
  /** Opaque keyset cursor taken from the previous page's next_cursor. Round-trip it untouched; never construct one. */
  cursor?: string;
};
export type CrmCreateCompanyApiResponse =
  /** status 201 Created company */ CrmCompany;
export type CrmCreateCompanyApiArg = {
  crmCompanyInput: CrmCompanyInput;
};
export type CrmGetCompanyApiResponse = /** status 200 Company */ CrmCompany;
export type CrmGetCompanyApiArg = {
  id: string;
};
export type CrmUpdateCompanyApiResponse =
  /** status 200 Updated company */ CrmCompany;
export type CrmUpdateCompanyApiArg = {
  id: string;
  crmCompanyInput: CrmCompanyInput;
};
export type CrmDeleteCompanyApiResponse = unknown;
export type CrmDeleteCompanyApiArg = {
  id: string;
};
export type CrmListCompanyContactsApiResponse =
  /** status 200 A page of the company's contacts */ CrmCompanyContactList;
export type CrmListCompanyContactsApiArg = {
  id: string;
  /** Page size. Defaults to 50, capped at 200. */
  limit?: number;
  /** Opaque keyset cursor taken from the previous page's next_cursor. Round-trip it untouched; never construct one. */
  cursor?: string;
};
export type CrmListCompanyDealsApiResponse =
  /** status 200 A page of the company's deals */ CrmDealList;
export type CrmListCompanyDealsApiArg = {
  id: string;
  /** Page size. Defaults to 50, capped at 200. */
  limit?: number;
  /** Opaque keyset cursor taken from the previous page's next_cursor. Round-trip it untouched; never construct one. */
  cursor?: string;
};
export type CrmListPipelinesApiResponse =
  /** status 200 Workspace pipelines */ CrmPipelineList;
export type CrmListPipelinesApiArg = void;
export type CrmCreatePipelineApiResponse =
  /** status 201 Created pipeline with default stages */ CrmPipeline;
export type CrmCreatePipelineApiArg = {
  crmPipelineInput: CrmPipelineInput;
};
export type CrmGetPipelineApiResponse = /** status 200 Pipeline */ CrmPipeline;
export type CrmGetPipelineApiArg = {
  id: string;
};
export type CrmUpdatePipelineApiResponse =
  /** status 200 Updated pipeline */ CrmPipeline;
export type CrmUpdatePipelineApiArg = {
  id: string;
  crmPipelineInput: CrmPipelineInput;
};
export type CrmDeletePipelineApiResponse = unknown;
export type CrmDeletePipelineApiArg = {
  id: string;
};
export type CrmCreateStageApiResponse =
  /** status 201 Created stage */ CrmStage;
export type CrmCreateStageApiArg = {
  id: string;
  crmStageInput: CrmStageInput;
};
export type CrmUpdateStageApiResponse =
  /** status 200 Updated stage */ CrmStage;
export type CrmUpdateStageApiArg = {
  id: string;
  stageId: string;
  crmStageInput: CrmStageInput;
};
export type CrmDeleteStageApiResponse = unknown;
export type CrmDeleteStageApiArg = {
  id: string;
  stageId: string;
};
export type CrmListDealsApiResponse =
  /** status 200 Workspace deals */ CrmDealList;
export type CrmListDealsApiArg = {
  /** Page size. Defaults to 50, capped at 200. */
  limit?: number;
  /** Opaque keyset cursor taken from the previous page's next_cursor. Round-trip it untouched; never construct one. */
  cursor?: string;
};
export type CrmCreateDealApiResponse = /** status 201 Created deal */ CrmDeal;
export type CrmCreateDealApiArg = {
  crmDealInput: CrmDealInput;
};
export type CrmGetDealApiResponse = /** status 200 Deal */ CrmDeal;
export type CrmGetDealApiArg = {
  id: string;
};
export type CrmUpdateDealApiResponse = /** status 200 Updated deal */ CrmDeal;
export type CrmUpdateDealApiArg = {
  id: string;
  crmDealInput: CrmDealInput;
};
export type CrmDeleteDealApiResponse = unknown;
export type CrmDeleteDealApiArg = {
  id: string;
};
export type CrmGetBoardApiResponse =
  /** status 200 Pipeline board with server-computed stage summaries */ CrmBoard;
export type CrmGetBoardApiArg = {
  pipelineId?: string;
};
export type CrmMoveDealApiResponse = /** status 200 Moved deal */ CrmDeal;
export type CrmMoveDealApiArg = {
  id: string;
  crmMoveDealInput: CrmMoveDealInput;
};
export type CrmListDealThreadsApiResponse =
  /** status 200 Threads linked to the deal */ CrmThreadList;
export type CrmListDealThreadsApiArg = {
  id: string;
};
export type CrmListEventsApiResponse =
  /** status 200 Append-only activity feed with adjacent ten-minute groups */ CrmEventList;
export type CrmListEventsApiArg = {
  targetType: "contact" | "company" | "deal";
  targetId: string;
};
export type CrmGetSettingsApiResponse =
  /** status 200 CRM workspace settings */ CrmSettings;
export type CrmGetSettingsApiArg = void;
export type CrmUpdateSettingsApiResponse =
  /** status 200 Updated settings */ CrmSettings;
export type CrmUpdateSettingsApiArg = {
  crmSettingsInput: CrmSettingsInput;
};
export type CrmListNotesApiResponse =
  /** status 200 Notes attached to the target */ CrmNoteList;
export type CrmListNotesApiArg = {
  targetType: "contact" | "company" | "deal";
  targetId: string;
  /** Page size. Defaults to 50, capped at 200. */
  limit?: number;
  /** Opaque keyset cursor taken from the previous page's next_cursor. Round-trip it untouched; never construct one. */
  cursor?: string;
};
export type CrmCreateNoteApiResponse = /** status 201 Created note */ CrmNote;
export type CrmCreateNoteApiArg = {
  crmNoteInput: CrmNoteInput;
};
export type CrmUpdateNoteApiResponse = /** status 200 Updated note */ CrmNote;
export type CrmUpdateNoteApiArg = {
  id: string;
  crmNoteUpdate: CrmNoteUpdate;
};
export type CrmDeleteNoteApiResponse = unknown;
export type CrmDeleteNoteApiArg = {
  id: string;
};
export type CrmListTasksApiResponse =
  /** status 200 Tasks attached to the target */ CrmTaskList;
export type CrmListTasksApiArg = {
  targetType: "contact" | "company" | "deal";
  targetId: string;
  /** Page size. Defaults to 50, capped at 200. */
  limit?: number;
  /** Opaque keyset cursor taken from the previous page's next_cursor. Round-trip it untouched; never construct one. */
  cursor?: string;
};
export type CrmCreateTaskApiResponse = /** status 201 Created task */ CrmTask;
export type CrmCreateTaskApiArg = {
  crmTaskInput: CrmTaskInput;
};
export type CrmUpdateTaskApiResponse = /** status 200 Updated task */ CrmTask;
export type CrmUpdateTaskApiArg = {
  id: string;
  crmTaskInput: CrmTaskInput;
};
export type CrmDeleteTaskApiResponse = unknown;
export type CrmDeleteTaskApiArg = {
  id: string;
};
export type CrmListContactEmailsApiResponse =
  /** status 200 Contact email aliases */ CrmContactEmailList;
export type CrmListContactEmailsApiArg = {
  id: string;
};
export type CrmAddContactEmailApiResponse =
  /** status 201 Added email alias */ CrmContactEmail;
export type CrmAddContactEmailApiArg = {
  id: string;
  crmContactEmailInput: CrmContactEmailInput;
};
export type CrmSetPrimaryContactEmailApiResponse = unknown;
export type CrmSetPrimaryContactEmailApiArg = {
  id: string;
  emailId: string;
};
export type ListReplyLabelsApiResponse =
  /** status 200 Reply labels */ ReplyLabelList;
export type ListReplyLabelsApiArg = void;
export type CreateReplyLabelApiResponse =
  /** status 201 Created label */ ReplyLabel;
export type CreateReplyLabelApiArg = {
  replyLabelInput: ReplyLabelInput;
};
export type ReorderReplyLabelsApiResponse =
  /** status 200 Reply labels in their new order */ ReplyLabelList;
export type ReorderReplyLabelsApiArg = {
  replyLabelReorderInput: ReplyLabelReorderInput;
};
export type UpdateReplyLabelApiResponse =
  /** status 200 Updated label */ ReplyLabel;
export type UpdateReplyLabelApiArg = {
  id: string;
  replyLabelInput: ReplyLabelInput;
};
export type DeleteReplyLabelApiResponse = unknown;
export type DeleteReplyLabelApiArg = {
  id: string;
};
export type ListInboxThreadsApiResponse =
  /** status 200 Threads in the workspace */ InboxThreadPage;
export type ListInboxThreadsApiArg = {
  /** Restrict to one mailbox. */
  mailboxId?: string;
  /** Restrict to one reply classification (e.g. positive, neutral, negative). */
  replyClass?: string;
  /** Case-insensitive substring search against the thread's subject or its linked contact's email. LIKE metacharacters (% and _) are matched literally, not as wildcards. */
  q?: string;
  /** Keyset cursor. Must be set together with before_id, or not at all. */
  beforeLastMessageAt?: string;
  /** Keyset cursor. Must be set together with before_last_message_at, or not at all. */
  beforeId?: string;
  /** Page size. Defaults to 25, capped at 200 (a larger request is clamped, not rejected). */
  limit?: number;
};
export type GetInboxThreadApiResponse =
  /** status 200 Thread with its full message history, oldest first */ InboxThreadDetail;
export type GetInboxThreadApiArg = {
  id: string;
};
export type SendInboxReplyApiResponse = unknown;
export type SendInboxReplyApiArg = {
  id: string;
  sendInboxReplyRequest: SendInboxReplyRequest;
};
export type DraftInboxReplyApiResponse =
  /** status 200 A suggested reply body */ InboxDraftReply;
export type DraftInboxReplyApiArg = {
  id: string;
};
export type SetInboxThreadReadApiResponse = unknown;
export type SetInboxThreadReadApiArg = {
  id: string;
  setInboxThreadReadRequest: SetInboxThreadReadRequest;
};
export type Membership = {
  workspace_id: string;
  workspace_name: string;
  role: string;
  /** When THIS workspace finished onboarding, or null while pending, so switching into a freshly created one needs no extra request. */
  onboarding_completed_at: string | null;
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
  /** When the ACTIVE workspace finished onboarding, or null while it is still pending. Present on the session response so the SPA can gate its onboarding modal on first paint without a second round trip. A nullable timestamp rather than a boolean beside it, so a response can never claim to be complete with no completion time. */
  onboarding_completed_at: string | null;
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
  /** When the caller's ACTIVE workspace finished onboarding, or null while pending. */
  onboarding_completed_at: string | null;
};
export type SwitchWorkspaceResponse = {
  access_token: string;
  expires_in: number;
  active_workspace_id: string;
  role: string;
  /** When the workspace just switched into finished onboarding, or null while pending. */
  onboarding_completed_at: string | null;
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
export type GoogleSignInStartResponse = {
  /** The Google consent URL to navigate the browser to. */
  auth_url: string;
};
export type GoogleSignInStartRequest = {
  /** Raw token from an invite link, when the invitee chose to accept it with Google rather than by setting a password. Only its hash is persisted server-side; it is never placed in the OAuth `state` parameter. */
  invite_token?: string | null;
  /** In-app path to land on after sign-in. Validated server-side as a same-origin path; anything else is silently dropped. */
  return_to?: string | null;
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
export type OnboardingResponse = {
  workspace_id: string;
  name: string;
  /** When onboarding was completed. Always set on a successful response. */
  onboarding_completed_at: string | null;
};
export type CompleteOnboardingRequest = {
  /** The workspace's real name, replacing the one derived at signup. */
  name: string;
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
  /** SENDER REPUTATION axis — the verdict on this mailbox's outbound mail, derived from inbox-placement and behavior signals. Carries no claim about pool eligibility; see lane for that. */
  health_state: "unknown" | "healthy" | "watch" | "throttled" | "paused";
  /** human-readable explanation of a non-healthy state */
  health_reason: string;
  /** POOL ELIGIBILITY axis — which peers this mailbox may exchange warmup traffic with (selection pairs same-lane only) and whether it may take new campaign leads. pending_auth = authentication and ownership not proven yet, no warmup and no new leads. probation = gathering evidence at a floor volume in its own lane, reduced leads. healthy = full participation. watch = reduced volume while a signal is diagnosed. recovery = earning its way back at a floor volume in its own lane. quarantine = withheld from the pool and new campaign leads blocked; exit needs fresh qualifying evidence, never elapsed time alone. blocked = withheld until an operator approves re-entry. Clients must treat an unrecognized value as unproven (probation), never as healthy. */
  lane:
    | "pending_auth"
    | "probation"
    | "healthy"
    | "watch"
    | "recovery"
    | "quarantine"
    | "blocked";
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
  /** SENDER REPUTATION axis (see WarmupParticipant.health_state) */
  health_state: "unknown" | "healthy" | "watch" | "throttled" | "paused";
  health_reason: string;
  /** POOL ELIGIBILITY axis (see WarmupParticipant.lane) */
  lane:
    | "pending_auth"
    | "probation"
    | "healthy"
    | "watch"
    | "recovery"
    | "quarantine"
    | "blocked";
  /** human-readable explanation of the current lane — what put the mailbox there and what clears it. Empty for a lane that needs no explanation. */
  lane_reason: string;
  today_sent: number;
  today_target: number;
  /** trusted inbox plus spam placement observations in the trailing 7 days */
  placement_sample_7d: number;
  /** of this mailbox's SENT warmup mail over 7 days, the fraction that landed in partners' inboxes (0..1); null when no placement was observed */
  inbox_rate_7d: number | null;
  /** of this mailbox's SENT warmup mail over 7 days, the fraction that landed in partners' spam (0..1); null when no placement was observed */
  spam_rate_7d: number | null;
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
  /** Enabled warmup participants counted on both warmup axes. unknown/healthy/watch/at_risk bucket the SENDER REPUTATION axis (health_state; at_risk = throttled + paused); probation and quarantine count the POOL ELIGIBILITY axis (lane). The two axes are independent, so a participant appears in one bucket per axis and the counts across axes overlap — only the reputation buckets sum to pool. */
  warmup: {
    pool: number;
    /** enabled participants without enough evidence for a health verdict */
    unknown: number;
    healthy: number;
    watch: number;
    at_risk: number;
    /** participants gathering evidence: lane in (probation, recovery) — sending at a floor volume in their own lane, not yet cleared for the healthy pool */
    probation: number;
    /** participants withheld from the pool: lane in (quarantine, blocked) — exchanging no warmup mail and blocked from taking new campaign leads. pending_auth is excluded; an unproven domain is already reported by the dmarc_failing attention row. */
    quarantine: number;
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
export type AiSettings = {
  default_smart_model: string;
  default_fast_model: string;
  /** empty array = every listed model is enabled */
  enabled_model_ids: string[];
  /** workspace-specific instructions appended to the agent system prompt */
  additional_instructions: string;
};
export type AiSettingsUpdate = {
  default_smart_model?: string;
  default_fast_model?: string;
  enabled_model_ids?: string[];
  additional_instructions?: string;
};
export type AiProviderKind =
  | "anthropic"
  | "bedrock"
  | "vertex_anthropic"
  | "openai"
  | "azure_openai"
  | "openai_compatible"
  | "google"
  | "vertex_google";
export type AiProviderConfig = {
  base_url?: string;
  endpoint?: string;
  api_version?: string;
  region?: string;
  project_id?: string;
};
export type AiProvider = {
  id: string;
  kind: AiProviderKind;
  display_name: string;
  config: AiProviderConfig;
  /** always true for a stored row */
  configured: boolean;
  /** masked credential identifier for display */
  key_prefix: string;
  created_at: string;
  updated_at: string;
};
export type AiProviderList = {
  providers: AiProvider[];
};
export type AiProviderCredentials = {
  api_key?: string;
  access_key_id?: string;
  secret_access_key?: string;
  service_account_json?: string;
};
export type AiProviderCreateRequest = {
  kind: AiProviderKind;
  display_name?: string;
  credentials: AiProviderCredentials;
  config?: AiProviderConfig;
};
export type AiProviderUpdateRequest = {
  display_name?: string;
  credentials?: AiProviderCredentials;
  config?: AiProviderConfig;
};
export type AiDiscoveredModel = {
  name: string;
  label?: string;
  context_window_tokens?: number;
  max_output_tokens?: number;
  input_cost_per_mtok?: number | null;
  output_cost_per_mtok?: number | null;
};
export type AiDiscoveryResult = {
  /** false = this kind has no discovery path yet; add models manually */
  supported: boolean;
  models: AiDiscoveredModel[];
};
export type AiModel = {
  /** composite "<provider_id>/<name>" */
  id: string;
  provider_id: string;
  kind: AiProviderKind;
  /** the bare model id the provider SDK receives */
  name: string;
  label: string;
  context_window_tokens: number;
  max_output_tokens: number;
  supports_reasoning: boolean;
  source: "catalog" | "custom";
  /** the deletable row id behind a source=custom entry (DELETE /ai/models/{id}); null for catalog entries */
  custom_model_id: string | null;
  /** informational USD per million input tokens */
  input_cost_per_mtok: number | null;
  output_cost_per_mtok: number | null;
  /** enabled_model_ids is empty OR contains this id */
  enabled: boolean;
};
export type AiModelList = {
  models: AiModel[];
};
export type AiModelCreateRequest = {
  provider_id: string;
  name: string;
  label: string;
  context_window_tokens: number;
  max_output_tokens: number;
  supports_reasoning?: boolean;
  input_cost_per_mtok?: number | null;
  output_cost_per_mtok?: number | null;
};
export type AgentPart = {
  id: string;
  order_index: number;
  type:
    "text" | "reasoning" | "tool_call" | "tool_result" | "compaction_notice";
  text?: string;
  reasoning?: string;
  tool_name?: string;
  tool_call_id?: string;
  tool_input?: {
    [key: string]: any;
  };
  tool_output?: any;
  state?: "" | "running" | "done" | "error" | "awaiting_approval";
  error?: string;
};
export type AgentMessage = {
  id: string;
  turn_id: string;
  role: "user" | "assistant";
  status: "sent" | "queued" | "processing";
  created_at: string;
  parts: AgentPart[];
};
export type AgentThread = {
  id: string;
  title: string;
  total_input_tokens: number;
  total_output_tokens: number;
  context_window_tokens: number;
  active_run_id: string | null;
  created_at: string;
  updated_at: string;
  messages?: AgentMessage[];
};
export type AgentThreadList = {
  threads: AgentThread[];
};
export type AgentSendResult = {
  message_id: string;
  run_id: string | null;
  queued: boolean;
};
export type AgentBrowsingContext = {
  type: "record_page" | "list_view";
  object?: string;
  record_id?: string;
  url?: string;
  view?: string;
  filters?: {
    [key: string]: string;
  };
};
export type AgentSendRequest = {
  text: string;
  /** composite model id or default-smart-model */
  model?: string;
  browsing_context?: AgentBrowsingContext;
};
export type AgentQueuedMessage = {
  id: string;
  text: string;
  model: string;
  created_at: string;
};
export type AgentQueue = {
  queued: AgentQueuedMessage[];
};
export type AgentApprovalStatus =
  "pending" | "approved" | "rejected" | "expired" | "executed" | "failed";
export type AgentApproval = {
  id: string;
  workspace_id: string;
  thread_id: string;
  run_id: string;
  tool_name: string;
  tool_call_id: string;
  arguments: {
    [key: string]: any;
  };
  edited_arguments?: {
    [key: string]: any;
  };
  risk_tier: "read" | "write" | "consequential" | "irreversible";
  status: AgentApprovalStatus;
  decision_reason?: string;
  expires_at: string;
  result?: any;
  error?: string;
  created_at: string;
  updated_at: string;
};
export type AgentApprovalList = {
  actions: AgentApproval[];
};
export type AgentApprovalDecisionRequest = {
  decision: "approve" | "reject";
  edited_arguments?: {
    [key: string]: any;
  };
  reason?: string;
};
export type List = {
  id?: string;
  name?: string;
};
export type ImportResult = {
  imported: number;
  /** Rows rejected outright: unreadable, or no valid email address. */
  skipped: number;
  duplicates: number;
  /** Custom field keys this file populated, so an operator can confirm their column landed somewhere. */
  mapped_fields: string[];
  /** Headers matching neither a built-in column nor a live custom field. These are reported rather than dropped in silence, which is what made a mis-named column impossible to diagnose before. */
  ignored_columns: string[];
  /** Cells rejected by their field's type (a "next week" in a date column). The ROW still imports - one bad cell should not cost the contact - so this is counted separately from `skipped`. */
  invalid_values: number;
};
export type Contact = {
  id?: string;
  email?: string;
  first_name?: string;
  last_name?: string;
  company_id?: string | null;
  company_name?: string;
  job_title?: string;
  linkedin_url?: string;
  deal_count?: number;
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
export type CustomFieldType = "text" | "number" | "date" | "select";
export type CustomFieldDef = {
  id: string;
  /** Lower-case identifier used as `{{custom.<key>}}` in sequence steps and as the CSV column name on import. */
  key: string;
  label: string;
  type: CustomFieldType;
  /** The allowed values for a `select`. Always present - an empty array for every other type - so a client never distinguishes null from absent. */
  options: string[];
  created_at: string;
  /** An archived field accepts no new values and no longer resolves in templates, but the values contacts already hold under it are untouched and still send. */
  archived: boolean;
  archived_at: string | null;
};
export type CustomFieldCreate = {
  key: string;
  label: string;
  type: CustomFieldType;
  /** Required for `select` (1-100 entries) and rejected for every other type. */
  options?: string[];
};
export type CustomFieldUpdate = {
  label: string;
  /** The select's full replacement option list. Rejected for a non-select field. */
  options?: string[];
};
export type CustomFieldValue = {
  key: string;
  /** Empty for a live field the contact has no value for. */
  value: string;
  /** Null when the key has no live definition - an archived field, or a value written before definitions existed. Render these read-only rather than hiding them. */
  def: CustomFieldDef | null;
};
export type CustomFieldValueSet = {
  /** The contact's COMPLETE live field set, keyed by field key. An omitted live key is cleared; an empty value clears its key. */
  values: {
    [key: string]: string;
  };
};
export type ContactSuppression = {
  /** The suppression list's own reason literal. `complaint` (they reported us as spam) is deliberately distinct from `unsubscribe` (they asked to stop) and is never collapsed into it. `bounce` here means a HARD bounce classified by the inbox poller - an ingested provider bounce feed does not suppress at all, because those include soft bounces (full mailbox, greylisting) and suppressing forever on a temporary failure is not recoverable. So a `bounce` on this list is a permanent delivery failure, not "a message bounced once". */
  reason: "unsubscribe" | "bounce" | "complaint" | "manual";
  /** The suppressed address, which is not necessarily the one sends would use. */
  email: string;
  /** Which of TWO different operational states this is - do not simplify it away to "an address is suppressed".
    
    True: the suppressed address is the contact's primary one, the address the send path actually resolves, so this person cannot be emailed at all. A hard stop, now.
    
    False: only a secondary alias is suppressed. Sending works today because it does not use that address - but promoting the alias to primary (PUT /crm/contacts/{id}/emails/{emailID}/primary) would silently stop sending. Reachable now, breakable by a routine edit.
    
    A boolean "suppressed" flag would merge those two into one answer that is wrong for whichever case it is not describing. */
  is_primary_email: boolean;
  suppressed_at: string;
};
export type ContactCompany = {
  id: string;
  name: string;
  /** Empty when the company has no domain on file. */
  domain: string;
};
export type ContactDeal = {
  id: string;
  name: string;
  pipeline_id: string;
  stage_id: string;
  stage_label: string;
  stage_color: string;
  stage_is_won: boolean;
  stage_is_lost: boolean;
  amount_micros?: number | null;
  currency: string;
  close_date?: string | null;
  created_at: string;
  updated_at: string;
};
export type ContactDetail = {
  id: string;
  /** The address the send path resolves for this contact. */
  email: string;
  first_name: string;
  last_name: string;
  job_title: string;
  linkedin_url: string;
  /** Null when no address of this contact is suppressed - i.e. they may be emailed. */
  suppression: ContactSuppression | null;
  /** Null when the contact is not linked to a company record. */
  company: ContactCompany | null;
  /** Deals in board order, capped at 25. A record page shows a roster, not a paginated list; the cap is what keeps this response bounded. */
  deals: ContactDeal[];
  /** The contact's TRUE deal total, counted independently of the capped `deals` list. Render "25 of 38" from this rather than treating the cap as the whole set. */
  deal_count: number;
  /** True when the contact has more deals than the cap and `deals` was cut short. */
  deals_truncated: boolean;
  created_at: string;
  updated_at: string;
};
export type ContactCompanyLink = {
  /** The company to link this contact to, or null to unlink. Must be a company in the caller's workspace; anything else is 404. */
  company_id: string | null;
};
export type ContactCampaignEnrollment = {
  campaign_id: string;
  campaign_name: string;
  /** Whether this campaign injected open/click tracking. This is the only thing that tells "nobody opened" apart from "opens were never recorded": a campaign with tracking off contributes to `emails_sent` but structurally cannot contribute to `opens_indicative` or `clicks`. The rollup's counts deliberately do not adjust for it - use this to explain a zero rather than to correct one.
    
    This is the PER-ENROLLMENT detail, for marking individual rows. Do not aggregate it to decide whether the summary's zeros were measured: the list is capped, so use the top-level `opens_measurable` for that. */
  tracking_enabled: boolean;
  status: "active" | "completed" | "stopped";
  /** 0 means enrolled but not yet sent to; N means step N was the last one sent. */
  current_step: number;
  /** Why the sequence stopped - one of replied, bounced, suppressed, manual, failed. `failed` is real (added by migration 000008 for a degenerate or exhausted send cap) and is deliberately NOT counted as a bounce or an unsubscribe in the rollup above. Null while the enrollment is still active or completed normally. Treat this as an open set: render an unrecognised literal verbatim rather than dropping it, so a future reason degrades visibly instead of vanishing. */
  stop_reason?: string | null;
  enrolled_at: string;
  last_sent_at?: string | null;
};
export type ContactEngagement = {
  contact_id: string;
  /** Sends with status 'sent' - the same numerator as the campaign rollup's stats.sent. */
  emails_sent: number;
  /** Distinct sends with an open event that is not a known prefetch (image proxy UA, or a fetch within two seconds of the send). Approximate by nature - clicks are the reliable signal. */
  opens_indicative: number;
  /** Distinct sends with at least one click event. */
  clicks: number;
  /** Enrollments stopped with reason 'replied'. */
  replies: number;
  /** Enrollments stopped with reason 'bounced'. */
  bounces: number;
  /** Enrollments stopped with reason 'suppressed'. */
  unsubscribes: number;
  /** opens_indicative / emails_sent, or 0 when nothing was sent. */
  open_rate: number;
  /** clicks / emails_sent, or 0 when nothing was sent. */
  click_rate: number;
  /** Enrollments over the contact's lifetime - active, completed, and stopped. */
  campaigns_enrolled: number;
  /** Whether an open COULD have been recorded for this contact: true when at least one send that actually went out belonged to a campaign with tracking enabled.
    
    Use it as `emails_sent > 0 && !opens_measurable` to render `opens_indicative` and `clicks` as "not measured" instead of 0. Do NOT derive this from `campaigns[].tracking_enabled`: that list is capped at 20, so for a contact with more enrollments whose newest are untracked and whose older ones were tracked, a client-side `some()` answers false and explains away a genuine zero. This field is computed over the whole send history and is correct at any enrollment count.
    
    Caveat: `campaigns.tracking_enabled` is mutable (PUT /campaigns/{id}/tracking), and no per-send record of the flag at send time exists, so this reflects each campaign's CURRENT setting. Toggling tracking off after a send can therefore turn a measured zero into an "unmeasured" one retroactively. Reporting the current setting is the best available answer, not a perfect one.
    
    When a non-zero count coexists with `opens_measurable: false`, trust the count - a recorded event outranks an inference about whether it could have been recorded. */
  opens_measurable: boolean;
  /** The most recent of the contact's last send and last tracking event. Null when neither has ever happened. */
  last_activity_at: string | null;
  /** The 20 most recently enrolled campaigns, newest first. */
  campaigns: ContactCampaignEnrollment[];
  /** True when the contact has more enrollments than the cap. The counts above stay exact regardless. */
  campaigns_truncated: boolean;
};
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
  /** This step's OWN share of its A/B split - the "A" side. Only meaningful alongside variants: a step with none sends its own content regardless of this value, matching the send path. Set it through PUT /campaigns/{id}/steps/{stepId}/base-weight, never through a step content edit. */
  variant_weight?: number;
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
  /** Narrower than daily_limit: caps how many BRAND-NEW contacts this campaign starts per UTC day. null means no limit. Counts only step-1 sends, so a sequence already in flight keeps sending its follow-ups on schedule regardless of how many new contacts started today. Bounded at 1000000 for the same 32-bit-integer reason as daily_limit. */
  max_new_leads_per_day?: number | null;
  /** Human-readable preview of the next few send instants this schedule produces, in its own timezone. */
  preview?: string[];
};
export type CampaignScheduleRequest = {
  timezone: string;
  days: SendWindowDay[];
  /** Campaign-wide sends per UTC day; null or omitted clears the limit. */
  daily_limit?: number | null;
  /** Brand-new contacts started per UTC day; null or omitted clears the limit. This is a full-replace PUT, so an omitted field clears it exactly like an explicit null. */
  max_new_leads_per_day?: number | null;
};
export type CampaignResultRow = {
  /** Null for the step's own base copy, which is also every send made before variants existed. */
  variant_id: string | null;
  /** "A" for the base copy, otherwise the variant's own label. */
  label: string;
  is_base: boolean;
  /** The arm share of the split. 0 for a retired arm that still has results. */
  weight: number;
  sent: number;
  /** Indicative opens: proxy-filtered, and structurally zero when tracking is off. */
  opens: number;
  clicks: number;
  replies: number;
  bounces: number;
  unsubscribes: number;
  open_rate: number;
  click_rate: number;
  reply_rate: number;
  bounce_rate: number;
  /** Every rate divides by this arm's own `sent`, never by enrollments - the numerator is per-arm, so a per-campaign denominator would let a rate exceed 1. */
  unsub_rate: number;
};
export type CampaignStepResults = {
  step_order: number;
  subject: string;
  rows: CampaignResultRow[];
  /** The label of the arm with the clearly-best REPLY rate, or null. Reply rate is the criterion because it is the only measure of what a cold email is for: opens are proxy-inflated and unmeasurable with tracking off, and clicks rank a variant for containing a link.
    
    Null far more often than not, deliberately. Naming a winner is an instruction to promote one arm and retire another, and doing that on noise costs a worse campaign plus a false belief about why. It stays null below 200 sends on the leading arm, with no replies anywhere, and when the leader is under 25% relatively ahead of the runner-up. */
  winner: string | null;
  /** Why there is no winner, so a null is never left to be interpreted. Empty both when a winner was named and when the step has a single arm (no comparison is pending). */
  winner_note: string;
};
export type CampaignResults = {
  campaign_id: string;
  steps: CampaignStepResults[];
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
  health_state?:
    ("unknown" | "healthy" | "watch" | "throttled" | "paused" | null) | null;
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
  /** Key of the reply label this reply was classified into. Deliberately an OPEN string, not an enum: a workspace defines its own labels (GET /reply-labels), and the key of a deleted custom label survives on historical enrollments, so clients must resolve it for display and degrade to showing the raw key. */
  reply_class: string | null;
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
export type StepVariant = {
  id: string;
  step_id: string;
  /** Short name shown as a column in the results table; unique per step. */
  label: string;
  /** Relative selection weight, against the step's own variant_weight and the other variants. 0 means "still here, no longer sending", which is how a losing arm is retired without orphaning the sends attributed to it. */
  weight: number;
  /** Empty on a follow-up step threads onto the previous message, exactly as the base copy does. */
  subject: string;
  body_text: string;
  body_html: string;
};
export type StepVariantRequest = {
  label: string;
  weight: number;
  subject?: string;
  body_text?: string;
  body_html?: string;
};
export type StepBaseWeightRequest = {
  weight: number;
};
export type ReorderStepsRequest = {
  /** the FULL ordered list of the campaign's step ids, in the desired order */
  step_ids: string[];
};
export type CampaignPreflightCheck = {
  /** `personalization_tokens` FAILS (does not warn) when a step contains a `{{...}}` placeholder nothing will substitute, which is harsher than the neighbouring content checks on purpose: an empty body is visible the moment an operator looks at it, whereas a bad token produces an email that looks fine in the editor and arrives reading "Hi {{firstname}}" or "Hi ,". A token nothing resolves is always a typo or a since-archived field, never an intent. */
  id:
    | "sequence_steps"
    | "empty_bodies"
    | "personalization_tokens"
    | "variant_weights"
    | "schedule_windows"
    | "sender_pool"
    | "audience"
    | "domain_auth"
    | "tracking"
    | "daily_limit"
    | "warmup_health";
  severity: "pass" | "warn" | "fail";
  title: string;
  detail: string;
  /** Empty string for a passing check. */
  remedy: string;
};
export type CampaignPreflight = {
  ready: boolean;
  checks: CampaignPreflightCheck[];
};
export type TestSendResponse = {
  queued: boolean;
};
export type TestSendRequest = {
  step_id: string;
  to: string;
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
export type CrmCompanyInput = {
  name: string;
  domain: string;
  owner_user_id?: string | null;
  annual_revenue_micros?: number | null;
  currency: string;
};
export type CrmCompany = CrmCompanyInput & {
  id: string;
  deal_count: number;
  created_at: string;
  updated_at: string;
};
export type CrmCompanyList = {
  items: CrmCompany[];
  /** Cursor for the next page. Absent on the last page. */
  next_cursor?: string;
};
export type CrmCompanyContact = {
  id: string;
  email: string;
  first_name: string;
  last_name: string;
  job_title: string;
  linkedin_url: string;
  created_at: string;
};
export type CrmCompanyContactList = {
  items: CrmCompanyContact[];
  /** Cursor for the next page. Absent on the last page. */
  next_cursor?: string;
};
export type CrmDealInput = {
  pipeline_id: string;
  stage_id: string;
  company_id?: string | null;
  primary_contact_id?: string | null;
  owner_user_id?: string | null;
  name: string;
  amount_micros?: number | null;
  currency: string;
  close_date?: string | null;
};
export type CrmDeal = CrmDealInput & {
  id: string;
  /** Fractional board ordering within the stage. Server-computed; change it with the move operation, never by writing it. */
  position: number;
  source: string;
  source_campaign_id?: string | null;
  source_thread_ref?: string;
  created_by_actor: {
    [key: string]: any;
  };
  pipeline_name: string;
  stage_label: string;
  stage_color: string;
  stage_is_won: boolean;
  stage_is_lost: boolean;
  company_name?: string;
  contact_email?: string;
  created_at: string;
  updated_at: string;
};
export type CrmDealList = {
  items: CrmDeal[];
  /** Cursor for the next page. Absent on the last page. */
  next_cursor?: string;
};
export type CrmStageInput = {
  label: string;
  color: string;
  position: number;
  is_won: boolean;
  is_lost: boolean;
};
export type CrmStage = CrmStageInput & {
  id: string;
  pipeline_id: string;
  key: string;
  created_at: string;
  updated_at: string;
};
export type CrmPipeline = {
  id: string;
  name: string;
  is_default: boolean;
  stages: CrmStage[];
  created_at: string;
  updated_at: string;
};
export type CrmPipelineList = {
  items: CrmPipeline[];
};
export type CrmPipelineInput = {
  name: string;
};
export type CrmBoardStage = {
  stage: CrmStage;
  deals: CrmDeal[];
  deal_count: number;
  amount_micros: number;
};
export type CrmBoard = {
  pipeline: CrmPipeline;
  stages: CrmBoardStage[];
};
export type CrmMoveDealInput = {
  stage_id: string;
  before_deal_id?: string | null;
  after_deal_id?: string | null;
};
export type CrmThreadParticipant = {
  email: string;
  display_name?: string;
  contact_id?: string | null;
};
export type CrmThreadMessage = {
  id: string;
  direction: "inbound" | "outbound";
  kind: "sent" | "reply";
  message_id?: string;
  sender_email?: string;
  recipient_email?: string;
  subject?: string;
  reply_class?: string;
  occurred_at: string;
};
export type CrmThread = {
  id: string;
  thread_ref: string;
  subject?: string;
  reply_class?: string;
  campaign_id?: string | null;
  contact_id?: string | null;
  last_message_at: string;
  participants: CrmThreadParticipant[];
  messages: CrmThreadMessage[];
};
export type CrmThreadList = {
  items: CrmThread[];
};
export type CrmEvent = {
  id: string;
  name: string;
  kind: string;
  object_type?: string;
  object_id?: string | null;
  contact_id?: string | null;
  company_id?: string | null;
  deal_id?: string | null;
  actor: {
    [key: string]: any;
  };
  data: {
    [key: string]: any;
  };
  linked_record_cached_name?: string;
  source_message_id?: string;
  source_thread_ref?: string;
  occurred_at: string;
  merged_count?: number;
};
export type CrmEventList = {
  items: CrmEvent[];
};
export type CrmSettingsInput = {
  auto_capture_policy: "sent_and_received" | "sent" | "off";
};
export type CrmSettings = CrmSettingsInput & {
  updated_at: string;
};
export type CrmNote = {
  id: string;
  title: string;
  body: string;
  created_by_actor: {
    [key: string]: any;
  };
  created_at: string;
  updated_at: string;
};
export type CrmNoteList = {
  items: CrmNote[];
  /** Cursor for the next page. Absent on the last page. */
  next_cursor?: string;
};
export type CrmNoteUpdate = {
  title: string;
  body: string;
};
export type CrmTargetFields = {
  target_type: "contact" | "company" | "deal";
  target_id: string;
};
export type CrmNoteInput = CrmNoteUpdate & CrmTargetFields;
export type CrmTask = {
  id: string;
  title: string;
  body: string;
  due_at?: string | null;
  status: "open" | "in_progress" | "done" | "cancelled";
  assignee_user_id?: string | null;
  created_by_actor: {
    [key: string]: any;
  };
  created_at: string;
  updated_at: string;
};
export type CrmTaskList = {
  items: CrmTask[];
  /** Cursor for the next page. Absent on the last page. */
  next_cursor?: string;
};
export type CrmTaskInput = CrmTargetFields & {
  title: string;
  body: string;
  due_at?: string | null;
  status: "open" | "in_progress" | "done" | "cancelled";
  assignee_user_id?: string | null;
};
export type CrmContactEmail = {
  id: string;
  contact_id: string;
  email: string;
  is_primary: boolean;
  created_at: string;
};
export type CrmContactEmailList = {
  items: CrmContactEmail[];
};
export type CrmContactEmailInput = {
  email: string;
};
export type ReplyLabelInput = {
  label: string;
  color: string;
  /** halt the sequence on this reply */
  stops_enrollment: boolean;
  /** machine-generated mail (out-of-office / auto-reply); never a human reply */
  is_automated: boolean;
  /** suppress the address, then stop (compliance) */
  suppresses_contact: boolean;
  /** open or update a CRM deal from this reply */
  captures_deal: boolean;
  /** Reschedule the next step past a return date stated in the body instead of stopping. When no date can be parsed the reply is only tagged - the sequence is never deferred on a guess. Deferrals are capped at 30 days. */
  defers_enrollment: boolean;
};
export type ReplyLabel = ReplyLabelInput & {
  id: string;
  /** stable machine key; the value stored on CampaignEnrollment.reply_class */
  key: string;
  position: number;
  /** Seeded with the workspace. May be renamed and have its flags changed, but never deleted - the classifier and historical enrollments both name its key. */
  is_builtin: boolean;
  created_at: string;
  updated_at: string;
};
export type ReplyLabelList = {
  labels: ReplyLabel[];
};
export type ReplyLabelReorderInput = {
  /** every label in the workspace, exactly once, in the new order */
  ids: string[];
};
export type InboxReplyLabelRef = {
  key: string;
  label: string;
  /** Hex color, #RRGGBB */
  color: string;
};
export type InboxThreadSummary = {
  id: string;
  mailbox_id: string;
  campaign_id: string | null;
  contact_id: string | null;
  /** The linked contact's email, or "" when contact_id is null (e.g. a legacy direct-send match). */
  contact_email: string;
  /** The linked contact's first name, or "" when contact_id is null. */
  contact_first_name: string;
  /** The linked contact's last name, or "" when contact_id is null. */
  contact_last_name: string;
  subject: string;
  last_reply_class: string;
  /** The workspace reply label resolved from last_reply_class for display, or null when the key no longer matches a label (readers degrade to the raw last_reply_class key). */
  reply_label: InboxReplyLabelRef | null;
  unread: boolean;
  last_message_at: string;
};
export type InboxThreadPage = {
  items: InboxThreadSummary[];
};
export type InboxMessage = {
  direction: "inbound" | "outbound";
  message_id: string;
  from_email: string;
  from_name: string;
  to_email: string;
  subject: string;
  body_text: string;
  body_html: string;
  reply_class: string;
  occurred_at: string;
};
export type InboxThreadDetail = InboxThreadSummary & {
  messages: InboxMessage[];
};
export type SendInboxReplyRequest = {
  body_text: string;
};
export type InboxDraftReply = {
  /** Suggested reply body, plain text, never empty and never HTML. The field name matches SendInboxReplyRequest.body_text so the client can hand the edited text straight back to the reply endpoint. */
  body_text: string;
};
export type SetInboxThreadReadRequest = {
  unread: boolean;
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
  useAuthGoogleSignInRedirectQuery,
  useAuthGoogleSignInStartMutation,
  useAuthGoogleSignInCallbackQuery,
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
  useCompleteWorkspaceOnboardingMutation,
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
  useGetAiSettingsQuery,
  useUpdateAiSettingsMutation,
  useListAiProvidersQuery,
  useCreateAiProviderMutation,
  useUpdateAiProviderMutation,
  useDeleteAiProviderMutation,
  useDiscoverAiProviderModelsMutation,
  useListAiModelsQuery,
  useCreateAiModelMutation,
  useDeleteAiModelMutation,
  useListAgentThreadsQuery,
  useCreateAgentThreadMutation,
  useGetAgentThreadQuery,
  useRenameAgentThreadMutation,
  useDeleteAgentThreadMutation,
  useSendAgentMessageMutation,
  useListAgentQueueQuery,
  useDeleteAgentQueuedMessageMutation,
  useStopAgentRunMutation,
  useStreamAgentThreadQuery,
  useListAgentApprovalsQuery,
  useGetAgentApprovalQuery,
  useDecideAgentApprovalMutation,
  useListListsQuery,
  useCreateListMutation,
  useRenameListMutation,
  useDeleteListMutation,
  useImportContactsMutation,
  useListContactsQuery,
  useListCustomFieldsQuery,
  useCreateCustomFieldMutation,
  useUpdateCustomFieldMutation,
  useArchiveCustomFieldMutation,
  useGetContactCustomFieldsQuery,
  useSetContactCustomFieldsMutation,
  useGetContactQuery,
  useSetContactCompanyMutation,
  useGetContactEngagementQuery,
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
  useGetCampaignResultsQuery,
  useExportCampaignResultsQuery,
  useGetCampaignSendersQuery,
  useUpdateCampaignSendersMutation,
  useListCampaignEnrollmentsQuery,
  useListStepsQuery,
  useCreateStepMutation,
  useUpdateStepMutation,
  useDeleteStepMutation,
  useListStepVariantsQuery,
  useCreateStepVariantMutation,
  useUpdateStepVariantMutation,
  useDeleteStepVariantMutation,
  useSetStepBaseWeightMutation,
  useReorderStepsMutation,
  useLaunchCampaignMutation,
  useGetCampaignPreflightQuery,
  useTestSendCampaignMutation,
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
  useCrmListCompaniesQuery,
  useCrmCreateCompanyMutation,
  useCrmGetCompanyQuery,
  useCrmUpdateCompanyMutation,
  useCrmDeleteCompanyMutation,
  useCrmListCompanyContactsQuery,
  useCrmListCompanyDealsQuery,
  useCrmListPipelinesQuery,
  useCrmCreatePipelineMutation,
  useCrmGetPipelineQuery,
  useCrmUpdatePipelineMutation,
  useCrmDeletePipelineMutation,
  useCrmCreateStageMutation,
  useCrmUpdateStageMutation,
  useCrmDeleteStageMutation,
  useCrmListDealsQuery,
  useCrmCreateDealMutation,
  useCrmGetDealQuery,
  useCrmUpdateDealMutation,
  useCrmDeleteDealMutation,
  useCrmGetBoardQuery,
  useCrmMoveDealMutation,
  useCrmListDealThreadsQuery,
  useCrmListEventsQuery,
  useCrmGetSettingsQuery,
  useCrmUpdateSettingsMutation,
  useCrmListNotesQuery,
  useCrmCreateNoteMutation,
  useCrmUpdateNoteMutation,
  useCrmDeleteNoteMutation,
  useCrmListTasksQuery,
  useCrmCreateTaskMutation,
  useCrmUpdateTaskMutation,
  useCrmDeleteTaskMutation,
  useCrmListContactEmailsQuery,
  useCrmAddContactEmailMutation,
  useCrmSetPrimaryContactEmailMutation,
  useListReplyLabelsQuery,
  useCreateReplyLabelMutation,
  useReorderReplyLabelsMutation,
  useUpdateReplyLabelMutation,
  useDeleteReplyLabelMutation,
  useListInboxThreadsQuery,
  useGetInboxThreadQuery,
  useSendInboxReplyMutation,
  useDraftInboxReplyMutation,
  useSetInboxThreadReadMutation,
} = injectedRtkApi;
