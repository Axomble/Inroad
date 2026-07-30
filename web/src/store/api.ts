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
export type UpdateCampaignTrackingApiResponse =
  /** status 200 Tracking flag updated */ {
    tracking_enabled?: boolean;
  };
export type UpdateCampaignTrackingApiArg = {
  id: string;
  updateCampaignTrackingRequest: UpdateCampaignTrackingRequest;
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
export type UpdateCampaignTrackingRequest = {
  enabled?: boolean;
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
  useListListsQuery,
  useCreateListMutation,
  useImportContactsMutation,
  useListContactsQuery,
  useListCampaignsQuery,
  useCreateCampaignMutation,
  useGetCampaignQuery,
  useUpdateCampaignTrackingMutation,
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
} = injectedRtkApi;
