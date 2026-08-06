// The scope vocabulary a workspace admin may grant to an API key. Mirrors the
// backend's single source of truth (`internal/app/auth/scopes.go` — AllScopes);
// the server re-validates every scope on create, so this list only shapes the
// picker. Grouped by domain for a legible multi-select.

export interface ScopeOption {
  value: string
  label: string
  description: string
}

export interface ScopeGroup {
  domain: string
  scopes: ScopeOption[]
}

export const API_KEY_SCOPE_GROUPS: readonly ScopeGroup[] = [
  {
    domain: 'Mailboxes',
    scopes: [
      { value: 'mailboxes:read', label: 'Read', description: 'View mailboxes and their status' },
      { value: 'mailboxes:write', label: 'Write', description: 'Connect, pause, and remove mailboxes' },
    ],
  },
  {
    domain: 'Campaigns',
    scopes: [
      { value: 'campaigns:read', label: 'Read', description: 'View campaigns and their sequences' },
      { value: 'campaigns:write', label: 'Write', description: 'Create and edit campaigns' },
      { value: 'campaigns:send', label: 'Send', description: 'Launch campaigns and send mail' },
    ],
  },
  {
    domain: 'Contacts',
    scopes: [
      { value: 'contacts:read', label: 'Read', description: 'View contacts' },
      { value: 'contacts:write', label: 'Write', description: 'Add and edit contacts' },
    ],
  },
  {
    domain: 'Lists',
    scopes: [
      { value: 'lists:read', label: 'Read', description: 'View contact lists' },
      { value: 'lists:write', label: 'Write', description: 'Create and edit contact lists' },
    ],
  },
  {
    domain: 'CRM',
    scopes: [
      { value: 'crm:read', label: 'Read', description: 'View companies, deals, pipelines, activities, and threads' },
      { value: 'crm:write', label: 'Write', description: 'Create, update, and move companies, deals, and pipelines' },
    ],
  },
] as const

/** Every grantable scope value, flattened — for validation/labelling. */
export const API_KEY_SCOPES: readonly string[] = API_KEY_SCOPE_GROUPS.flatMap((g) =>
  g.scopes.map((s) => s.value),
)
