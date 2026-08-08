/**
 * The docs hub's content, transcribed from the code that is its source of
 * truth — every entry here must stay checkable against the file it cites:
 *
 * - MCP tools:   internal/app/agenttool/registry.go + tools_*.go (names, risk,
 *                min role) and internal/app/mcpserver/server.go requiredScope()
 *                (scope column).
 * - Env vars:    internal/platform/config/config.go + .env.example.
 * - Guides:      docs/src/content/docs/** (the Starlight docs site source).
 */

// Mirrors agenttool.Risk (internal/app/agenttool/agenttool.go). No registered
// tool is currently in the irreversible tier; it exists as the always-approval
// backstop for future send/destroy tools.
export type ToolRisk = 'read' | 'write' | 'consequential' | 'irreversible'

export interface McpTool {
  name: string
  /** OAuth scope the grant must carry for the tool to appear (mcpserver requiredScope). */
  scope: string
  risk: ToolRisk
  /** Requires the workspace admin role on top of the scope (Tool.MinRole). */
  adminOnly?: boolean
  description: string
}

// Name-sorted, matching the registry's own stable ordering.
export const MCP_TOOLS: McpTool[] = [
  {
    name: 'inroad_campaign_control',
    scope: 'campaigns:write',
    risk: 'consequential',
    adminOnly: true,
    description: 'Pause or resume a running campaign. Not reachable over MCP: campaigns:write is never OAuth-grantable.',
  },
  {
    name: 'inroad_campaign_read',
    scope: 'campaigns:read',
    risk: 'read',
    description: 'List campaigns, read one campaign’s configuration and stats, and see enrollments and replies.',
  },
  {
    name: 'inroad_company_read',
    scope: 'crm:read',
    risk: 'read',
    description: 'List CRM companies or get one company by id.',
  },
  {
    name: 'inroad_company_write',
    scope: 'crm:write',
    risk: 'write',
    description: 'Create or update a company in the workspace.',
  },
  {
    name: 'inroad_contact_read',
    scope: 'contacts:read',
    risk: 'read',
    description: 'Search contacts by name or email fragment, or browse them, optionally within one list.',
  },
  {
    name: 'inroad_contact_write',
    scope: 'contacts:write',
    risk: 'write',
    description: 'Create a contact, or add an existing contact to a contact list.',
  },
  {
    name: 'inroad_contacts_import',
    scope: 'contacts:write',
    risk: 'consequential',
    description: 'Bulk-import more than 50 contacts into a list; every call parks for human review before executing.',
  },
  {
    name: 'inroad_deal_read',
    scope: 'crm:read',
    risk: 'read',
    description: 'List deals, get one deal, or read a pipeline board with stage totals.',
  },
  {
    name: 'inroad_deal_write',
    scope: 'crm:write',
    risk: 'write',
    description: 'Create, update, or move a deal between pipeline stages.',
  },
  {
    name: 'inroad_deliverability_read',
    scope: 'campaigns:read',
    risk: 'read',
    description: 'Read sending health: the live pulse snapshot, the deliverability score breakdown, and at-risk mailboxes.',
  },
  {
    name: 'inroad_events_read',
    scope: 'crm:read',
    risk: 'read',
    description: 'Read the attributed CRM activity feed for a contact, company, or deal.',
  },
  {
    name: 'inroad_list_read',
    scope: 'lists:read',
    risk: 'read',
    description: 'Read the workspace’s contact lists (campaign audiences), including member counts.',
  },
  {
    name: 'inroad_list_write',
    scope: 'lists:write',
    risk: 'write',
    description: 'Create an empty contact list to add contacts into.',
  },
  {
    name: 'inroad_mailbox_read',
    scope: 'mailboxes:read',
    risk: 'read',
    description: 'Read connected sending mailboxes: status, daily cap, minimum interval, and ramp settings.',
  },
  {
    name: 'inroad_note_write',
    scope: 'crm:write',
    risk: 'write',
    description: 'Create a note attached to a contact, company, or deal.',
  },
  {
    name: 'inroad_pipeline_read',
    scope: 'crm:read',
    risk: 'read',
    description: 'Read CRM pipelines and their ordered stages.',
  },
  {
    name: 'inroad_search',
    scope: 'contacts:read',
    risk: 'read',
    description: 'Cross-record search by name or email: campaigns, contacts, mailboxes, and lists in one call.',
  },
  {
    name: 'inroad_task_write',
    scope: 'crm:write',
    risk: 'write',
    description: 'Create a follow-up task attached to a contact, company, or deal.',
  },
  {
    name: 'inroad_thread_read',
    scope: 'crm:read',
    risk: 'read',
    description: 'Read structured thread metadata for a deal — participants and message metadata, never raw bodies.',
  },
  {
    name: 'inroad_warmup_read',
    scope: 'mailboxes:read',
    risk: 'read',
    description: 'Read warmup progress: per-mailbox ramp health and rolling inbox/spam placement rates.',
  },
]

/** auth.OAuthGrantableScopes — the strict subset an OAuth client may ever hold. */
export const OAUTH_GRANTABLE_SCOPES = [
  'mailboxes:read',
  'campaigns:read',
  'contacts:read',
  'contacts:write',
  'crm:read',
  'crm:write',
  'lists:read',
  'lists:write',
  'inbox:write',
]

export interface EnvVar {
  name: string
  description: string
  example?: string
}

export interface EnvGroup {
  title: string
  vars: EnvVar[]
}

// A curated subset of internal/platform/config/config.go — the variables a
// self-hoster actually decides about. .env.example documents the full set.
export const ENV_GROUPS: EnvGroup[] = [
  {
    title: 'Required',
    vars: [
      { name: 'INROAD_DATABASE_URL', description: 'PostgreSQL connection URL.', example: 'postgres://inroad:inroad@postgres:5432/inroad' },
      { name: 'INROAD_REDIS_ADDR', description: 'Redis host and port.', example: 'redis:6379' },
      { name: 'INROAD_JWT_SECRET', description: 'Access-token signing secret.', example: 'openssl rand -base64 32' },
      { name: 'INROAD_MASTER_KEY', description: 'Key-encryption key (base64, 32 bytes) that wraps per-workspace credential keys. Losing it loses every stored secret.', example: 'openssl rand -base64 32' },
      { name: 'INROAD_PUBLIC_URL', description: 'Canonical public URL of the deployment; OAuth redirect URLs default from it.', example: 'https://inroad.example.com' },
    ],
  },
  {
    title: 'Server',
    vars: [
      { name: 'INROAD_HTTP_ADDR', description: 'API listen address.', example: ':8080' },
      { name: 'INROAD_WEB_DIR', description: 'Directory of the built SPA the API serves; blank when Vite runs separately.' },
      { name: 'INROAD_METRICS_ADDR', description: 'Dedicated Prometheus /metrics listener for API and worker; blank disables it.', example: ':9091' },
      { name: 'INROAD_TRUSTED_PROXIES', description: 'Comma-separated CIDRs of reverse proxies whose X-Forwarded-For is trusted; blank trusts nothing.', example: '10.0.0.0/8' },
      { name: 'INROAD_LOG_LEVEL', description: 'Logging level: debug, info, warn, or error.', example: 'info' },
    ],
  },
  {
    title: 'Auth & security',
    vars: [
      { name: 'INROAD_ACCESS_TOKEN_TTL', description: 'Access-token lifetime; sessions are revalidated against the store, so revokes act fast regardless.', example: '5m' },
      { name: 'INROAD_REFRESH_TOKEN_TTL', description: 'Refresh-token lifetime.', example: '720h' },
      { name: 'INROAD_COOKIE_SECURE', description: 'Set the auth cookies with the Secure attribute (turn on behind HTTPS).', example: 'true' },
      { name: 'INROAD_TURNSTILE_SECRET', description: 'Cloudflare Turnstile secret guarding register/login; blank disables the captcha gate.' },
      { name: 'INROAD_RP_ID', description: 'WebAuthn relying-party domain when the SPA origin differs from the API’s public URL.', example: 'app.example.com' },
      { name: 'INROAD_TRACKING_SECRET', description: 'Signs open/click tracking tokens; falls back to the JWT secret when unset.' },
    ],
  },
  {
    title: 'Google sign-in',
    vars: [
      {
        name: 'INROAD_GOOGLE_SIGNIN_CLIENT_ID',
        description:
          'Google OAuth client for “Continue with Google” on login/signup; blank disables it and the button hides itself. Deliberately separate from the mailbox-connect client below — this one requests only openid/email/profile, never Gmail scopes.',
      },
      { name: 'INROAD_GOOGLE_SIGNIN_CLIENT_SECRET', description: 'Google sign-in client secret.' },
      {
        name: 'INROAD_GOOGLE_SIGNIN_REDIRECT_URL',
        description:
          'Add this exact URL to the OAuth client’s authorized redirect URIs. Defaults to the public URL’s /api/v1/auth/oauth/google/callback.',
        example: 'https://app.example.com/api/v1/auth/oauth/google/callback',
      },
    ],
  },
  {
    title: 'Mailbox OAuth connect',
    vars: [
      { name: 'INROAD_GOOGLE_CLIENT_ID', description: 'Google OAuth client for connecting Gmail mailboxes; blank disables Gmail connect.' },
      { name: 'INROAD_GOOGLE_CLIENT_SECRET', description: 'Google OAuth client secret.' },
      { name: 'INROAD_MS_CLIENT_ID', description: 'Microsoft Entra app for connecting Microsoft 365 mailboxes; blank disables M365 connect.' },
      { name: 'INROAD_MS_CLIENT_SECRET', description: 'Microsoft OAuth client secret.' },
      { name: 'INROAD_MS_TENANT', description: 'Entra tenant/authority; "common" (the default) allows any tenant.' },
    ],
  },
  {
    title: 'AI & agent',
    vars: [
      { name: 'INROAD_AI_ALLOW_PRIVATE_BASE_URL', description: 'Allow AI-provider base URLs on private/loopback hosts (a local Ollama/vLLM). Off by default.', example: 'false' },
      { name: 'INROAD_AGENT_MAX_CONCURRENT_RUNS', description: 'Concurrent agent runs one API process executes.', example: '20' },
    ],
  },
  {
    title: 'Worker',
    vars: [
      { name: 'INROAD_WORKER_CONCURRENCY', description: 'Concurrent asynq goroutines per worker; raise past 10 once you run ~50+ active mailboxes.', example: '10' },
      { name: 'INROAD_WORKER_ID', description: 'Stable worker identity; defaults to the hostname and names its dedicated queue.' },
      { name: 'INROAD_WORKER_EGRESS_IP', description: 'Source IP outbound SMTP/IMAP dials bind to, so a mailbox’s mail egresses from one IP.' },
    ],
  },
]

const DOCS_BASE = 'https://github.com/inroad/inroad/blob/main/docs/src/content/docs'

export interface GuideLink {
  title: string
  description: string
  href: string
}

export interface GuideGroup {
  title: string
  guides: GuideLink[]
}

export const GUIDE_GROUPS: GuideGroup[] = [
  {
    title: 'Features',
    guides: [
      { title: 'Campaigns & sequences', description: 'Multi-step cadences, enrollment, scheduling, and lifecycle.', href: `${DOCS_BASE}/guides/campaigns.md` },
      { title: 'Mailboxes & connections', description: 'Connecting Gmail / Microsoft 365 / SMTP senders and how credentials are protected.', href: `${DOCS_BASE}/guides/mailboxes.md` },
      { title: 'Warmup', description: 'How the warmup engine ramps a mailbox and measures placement.', href: `${DOCS_BASE}/guides/warmup.md` },
      { title: 'Deliverability', description: 'The deliverability score, suppressions, and the sending circuit breaker.', href: `${DOCS_BASE}/guides/deliverability.md` },
      { title: 'Reply classification', description: 'How inbound replies are labeled and what each label does to an enrollment.', href: `${DOCS_BASE}/guides/reply-classification.md` },
      { title: 'Unified inbox', description: 'Reading campaign replies across every connected mailbox.', href: `${DOCS_BASE}/guides/unified-inbox.md` },
      { title: 'CRM & contacts', description: 'Companies, deals, pipelines, and contact lists.', href: `${DOCS_BASE}/guides/crm-contacts.md` },
      { title: 'Auth & security', description: 'Sessions, 2FA, passkeys, API keys, and the OAuth provider.', href: `${DOCS_BASE}/guides/auth-security.md` },
    ],
  },
  {
    title: 'Deploy',
    guides: [
      { title: 'Docker Compose', description: 'The reference self-hosted deployment.', href: `${DOCS_BASE}/deploy/docker-compose.md` },
      { title: 'Environment variables', description: 'The full grouped configuration reference.', href: `${DOCS_BASE}/deploy/environment-variables.md` },
      { title: 'AWS (Terraform)', description: 'Running the control and execution planes on AWS.', href: `${DOCS_BASE}/deploy/aws-terraform.md` },
      { title: 'Kubernetes (Helm)', description: 'Running on a cluster.', href: `${DOCS_BASE}/deploy/kubernetes-helm.md` },
    ],
  },
  {
    title: 'MCP & agents',
    guides: [
      { title: 'MCP server overview', description: 'Endpoint, transport, and the OAuth 2.1 authentication flow.', href: `${DOCS_BASE}/mcp/index.md` },
      { title: 'Tool reference', description: 'Every tool with its scope and risk tier.', href: `${DOCS_BASE}/mcp/tool-reference.md` },
      { title: 'Claude Desktop', description: 'Connecting Claude Desktop to /v1/mcp.', href: `${DOCS_BASE}/mcp/claude-desktop.md` },
      { title: 'Cursor & Windsurf', description: 'Connecting IDE agents.', href: `${DOCS_BASE}/mcp/cursor-windsurf.md` },
      { title: 'LangChain & SDKs', description: 'Programmatic MCP clients.', href: `${DOCS_BASE}/mcp/langchain-sdks.md` },
    ],
  },
]
