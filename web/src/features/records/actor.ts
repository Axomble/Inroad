import { Bot, MessageSquareText, UserRound, type LucideIcon } from 'lucide-react'
import { z } from 'zod'

/**
 * Who created a CRM record. `created_by_actor` is an open JSON object in the
 * API contract (`CrmDeal['created_by_actor']` is a bare record), so the wire
 * shape is validated here once rather than trusted at each render site. Field
 * names stay snake_case because they are the JSON boundary's own names.
 */
const actorSchema = z.object({
  type: z.string().optional(),
  client_id: z.string().optional(),
  on_behalf_of_user_id: z.string().optional(),
  thread_id: z.string().optional(),
  run_id: z.string().optional(),
}).passthrough()

/** The actor kinds the backend stamps; anything else is treated as `system`. */
export type ActorKind = 'agent' | 'user' | 'system'

export type Actor = {
  type: ActorKind
  client_id?: string
  on_behalf_of_user_id?: string
  thread_id?: string
  run_id?: string
}

/**
 * Parse an untyped `created_by_actor` / `event.actor` value. Malformed or
 * absent input falls back to a system actor: attribution that cannot be proven
 * is never attributed to a person.
 */
export function parseActor(raw: unknown): Actor {
  const result = actorSchema.safeParse(raw)
  if (!result.success) return { type: 'system' }
  const { type, client_id, on_behalf_of_user_id, thread_id, run_id } = result.data
  return {
    type: type === 'agent' || type === 'user' ? type : 'system',
    client_id,
    on_behalf_of_user_id,
    thread_id,
    run_id,
  }
}

/**
 * How a record came to exist. The actor is the primary evidence; `Deal.source`
 * (`manual | reply | agent`) is a secondary signal that only decides the case
 * the actor leaves open — a system-stamped deal captured from a reply.
 */
export type ActorOrigin = ActorKind | 'reply'

export function actorOrigin(actor: Actor, source?: string): ActorOrigin {
  if (actor.type === 'agent' || actor.type === 'user') return actor.type
  if (source === 'agent') return 'agent'
  if (source === 'reply') return 'reply'
  return 'system'
}

/**
 * Per-origin label, icon and tint. The label is the signal — color and icon
 * only reinforce it — so origin stays readable for colorblind users and in
 * both themes (the tints come from the shared tokens, never hardcoded greys).
 */
export const originMeta: Record<ActorOrigin, { label: string; why: string; icon: LucideIcon; tone: string }> = {
  agent: {
    label: 'Agent',
    why: 'Created by an AI agent acting in this workspace.',
    icon: Bot,
    tone: 'bg-security/12 text-security',
  },
  user: {
    label: 'Workspace member',
    why: 'Created by a workspace member.',
    icon: UserRound,
    tone: 'bg-surface-2 text-muted-foreground',
  },
  reply: {
    label: 'Auto-captured',
    why: 'Captured automatically from a positive campaign reply.',
    icon: MessageSquareText,
    tone: 'bg-data/12 text-data',
  },
  system: {
    label: 'Inroad automation',
    why: 'Created by Inroad automation.',
    icon: UserRound,
    tone: 'bg-surface-2 text-muted-foreground',
  },
}

/**
 * The one-line explanation shown as the badge's `title` — the codebase's
 * cheapest explain-this-flag affordance (see `HealthBadge`). For an agent it
 * carries the client and the thread/run that produced the record, which is the
 * only place a reader can tie a CRM row back to an agent run.
 */
export function actorTitle(actor: Actor, source?: string): string {
  const parts = [originMeta[actorOrigin(actor, source)].why]
  if (actor.client_id) parts.push(`Client ${actor.client_id}.`)
  if (actor.on_behalf_of_user_id) parts.push(`On behalf of user ${actor.on_behalf_of_user_id}.`)
  if (actor.thread_id) parts.push(`Agent thread ${actor.thread_id}${actor.run_id ? ` / run ${actor.run_id}` : ''}.`)
  return parts.join(' ')
}
