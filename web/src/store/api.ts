import { emptyApi as api } from "./empty-api";
const injectedRtkApi = api.injectEndpoints({
  endpoints: (build) => ({
    authRegister: build.mutation<AuthRegisterApiResponse, AuthRegisterApiArg>({
      query: (queryArg) => ({
        url: `/auth/register`,
        method: "POST",
        body: queryArg.registerRequest,
      }),
    }),
    authLogin: build.mutation<AuthLoginApiResponse, AuthLoginApiArg>({
      query: (queryArg) => ({
        url: `/auth/login`,
        method: "POST",
        body: queryArg.loginRequest,
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
          limit: queryArg.limit,
          offset: queryArg.offset,
        },
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
  }),
  overrideExisting: false,
});
export { injectedRtkApi as api };
export type AuthRegisterApiResponse = /** status 200 Session */ SessionResponse;
export type AuthRegisterApiArg = {
  registerRequest: RegisterRequest;
};
export type AuthLoginApiResponse = /** status 200 Session */ SessionResponse;
export type AuthLoginApiArg = {
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
export type ListContactsApiResponse = /** status 200 Contacts */ Contact[];
export type ListContactsApiArg = {
  list: string;
  limit?: number;
  offset?: number;
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
};
export type RegisterRequest = {
  workspace_name: string;
  email: string;
  password: string;
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
  use_tls?: boolean;
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
  use_tls?: boolean;
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
export type CampaignDetail = {
  id?: string;
  name?: string;
  subject?: string;
  status?: string;
  /** send counts by status */
  stats?: {
    [key: string]: number;
  };
  /** enrollment counts by status */
  enrollments?: {
    [key: string]: number;
  };
  steps?: SequenceStep[];
};
export type StepRequest = {
  delay_seconds?: number;
  subject?: string;
  body_text?: string;
  body_html?: string;
};
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
  useCreateWorkspaceInviteMutation,
  useListWorkspaceInvitesQuery,
  useRevokeWorkspaceInviteMutation,
  useListMailboxesQuery,
  useConnectMailboxMutation,
  useGetMailboxQuery,
  useDeleteMailboxMutation,
  usePauseMailboxMutation,
  useResumeMailboxMutation,
  useListListsQuery,
  useCreateListMutation,
  useImportContactsMutation,
  useListContactsQuery,
  useListCampaignsQuery,
  useCreateCampaignMutation,
  useGetCampaignQuery,
  useListStepsQuery,
  useCreateStepMutation,
  useUpdateStepMutation,
  useDeleteStepMutation,
  useLaunchCampaignMutation,
  useUnsubscribeConfirmPageQuery,
  useUnsubscribeMutation,
} = injectedRtkApi;
