// The audit log's closed vocabularies — actions, their categories, actor types —
// named as an operator would say them.
//
// The generated client exports these as type unions only, with no runtime list,
// so the label tables below ARE the runtime list. Each is keyed by the generated
// union, which is what keeps it honest: a new action added to the contract fails
// the typecheck here until it is given a label, rather than rendering as a raw
// dotted name nobody noticed.
//
// Deliberately free of React and of any fetching: the route's `validateSearch`
// imports this module, and the route file sits outside the lazily split chunk.
import type { AuditAction, AuditActorType } from '@/store/api'

/** The segment before the first dot — what the API's `action` prefix filter matches on. */
export type AuditCategory = AuditAction extends `${infer Prefix}.${string}` ? Prefix : never

const ACTION_LABELS: Record<AuditAction, string> = {
  'auth.login': 'Signed in',
  'auth.login_failed': 'Sign-in failed',
  'member.invited': 'Member invited',
  'member.invite_revoked': 'Invite revoked',
  'member.joined': 'Member joined',
  'member.role_changed': 'Role changed',
  'mailbox.connected': 'Mailbox connected',
  'mailbox.disconnected': 'Mailbox disconnected',
  'mailbox.paused': 'Mailbox paused',
  'mailbox.resumed': 'Mailbox resumed',
  'campaign.started': 'Campaign started',
  'campaign.paused': 'Campaign paused',
  'campaign.resumed': 'Campaign resumed',
  'apikey.created': 'API key created',
  'apikey.revoked': 'API key revoked',
  'data.exported': 'Data exported',
  'settings.changed': 'Settings changed',
}

const CATEGORY_LABELS: Record<AuditCategory, string> = {
  auth: 'Sign-in',
  member: 'Team',
  mailbox: 'Mailboxes',
  campaign: 'Campaigns',
  apikey: 'API keys',
  data: 'Data export',
  settings: 'Settings',
}

const ACTOR_TYPE_LABELS: Record<AuditActorType, string> = {
  user: 'People',
  api_key: 'API keys',
  oauth_client: 'Connected apps',
  agent: 'Agents',
  system: 'System',
}

/**
 * Actions grouped under their category, in the order the tables above declare
 * them — which is the order the action filter lists them in.
 */
export interface ActionGroup {
  category: AuditCategory
  label: string
  actions: readonly { action: AuditAction; label: string }[]
}

function categoryOf(action: AuditAction): AuditCategory {
  // The template-literal type guarantees a dot, so the prefix is a category by
  // construction; the cast only restates what AuditCategory already derives.
  return action.slice(0, action.indexOf('.')) as AuditCategory
}

export const ACTION_GROUPS: readonly ActionGroup[] = (Object.keys(CATEGORY_LABELS) as AuditCategory[]).map(
  (category) => ({
    category,
    label: CATEGORY_LABELS[category],
    actions: (Object.keys(ACTION_LABELS) as AuditAction[])
      .filter((action) => categoryOf(action) === category)
      .map((action) => ({ action, label: ACTION_LABELS[action] })),
  }),
)

export const ACTOR_TYPES: readonly { actorType: AuditActorType; label: string }[] = (
  Object.keys(ACTOR_TYPE_LABELS) as AuditActorType[]
).map((actorType) => ({ actorType, label: ACTOR_TYPE_LABELS[actorType] }))

/**
 * Whether a value is something the `action` filter can send: a whole action, or
 * a category the server matches as a prefix on a segment boundary.
 */
export function isActionFilter(value: string): boolean {
  return Object.hasOwn(ACTION_LABELS, value) || Object.hasOwn(CATEGORY_LABELS, value)
}

export function isActorType(value: string): value is AuditActorType {
  return Object.hasOwn(ACTOR_TYPE_LABELS, value)
}

/**
 * The human label for an action. Takes a plain string on purpose: a newer
 * server can send an action this build has no label for, and the honest render
 * of that is the raw name — not a blank cell, and not a guessed label.
 */
export function actionLabel(action: string): string {
  return Object.hasOwn(ACTION_LABELS, action) ? ACTION_LABELS[action as AuditAction] : action
}
