// Draft state and validation for the sender-pool editor. Component-free so the
// rules live in one place and are unit-tested directly, and so the panel file
// only exports components (fast refresh).
import { httpStatus, isFetchBaseQueryError } from '@/lib/rtk-error'
import type { CampaignSenderPool, CampaignSenderPoolRequest, RotationMode } from './api'
import type { Mailbox } from '@/store/api'

/**
 * One editable row. `weight` holds the raw input string, not a number: a
 * half-cleared field must stay invalid rather than being coerced to 0 or 1 and
 * silently saved. `included` is client-only — it means "this mailbox is in the
 * pool at all", which the API expresses as presence in the `senders` array.
 */
export type DraftSender = {
  mailbox_id: string
  email: string
  provider?: string
  status?: string
  included: boolean
  weight: string
  enabled: boolean
  /** Read-only rotation state, straight from the server. */
  assignedCount: number
  lastAssignedAt: string | null
}

/** The rotation modes, with the copy that explains the selected one. */
export const ROTATION_MODES: readonly { value: RotationMode; label: string; description: string }[] = [
  {
    value: 'weighted',
    label: 'Weighted (recommended)',
    description:
      'Scores each mailbox by remaining daily capacity, warmup health, and age, so the mailbox best able to carry volume takes more of it. Weight multiplies that score.',
  },
  {
    value: 'round_robin',
    label: 'Round robin',
    description: 'Always picks the mailbox with the fewest contacts so far, evening out the contact count.',
  },
  {
    value: 'least_recently_used',
    label: 'Least recently used',
    description:
      'Always picks the mailbox that has gone longest without an assignment — never-used mailboxes first — spreading sending over time.',
  },
]

/** The explanation for the selected mode, so the selector is never bare jargon. */
export function rotationModeDescription(mode: RotationMode): string {
  return ROTATION_MODES.find((m) => m.value === mode)?.description ?? ''
}

/**
 * Builds the editor rows: every mailbox the operator could send from, marked
 * with whether it's currently in the pool.
 *
 * The row set is the union of the workspace's active mailboxes and the pool's
 * own members. A pool member that has since been paused or is otherwise not in
 * the active list still gets a row — dropping it would quietly delete it from the
 * pool on the next save, since the PUT is a full replace.
 */
export function toDraft(
  pool: CampaignSenderPool | undefined,
  mailboxes: Mailbox[] | undefined,
): DraftSender[] {
  const members = new Map((pool?.senders ?? []).map((s) => [s.mailbox_id, s]))
  const rows = new Map<string, DraftSender>()

  for (const mailbox of mailboxes ?? []) {
    if (!mailbox.id) continue // a row with no id can't be sent back; skip rather than send ''
    const member = members.get(mailbox.id)
    if (!member && mailbox.status !== 'active') continue // only active mailboxes are offerable
    rows.set(mailbox.id, {
      mailbox_id: mailbox.id,
      email: mailbox.email ?? member?.email ?? mailbox.id,
      provider: mailbox.provider ?? member?.provider,
      status: mailbox.status ?? member?.status,
      included: member !== undefined,
      weight: String(member?.weight ?? 1),
      enabled: member?.enabled ?? true,
      assignedCount: member?.assigned_count ?? 0,
      lastAssignedAt: member?.last_assigned_at ?? null,
    })
  }

  // Pool members the mailbox list didn't return at all (deleted, or not visible
  // for some other reason) are still shown from the pool's read-only fields.
  for (const [mailboxId, member] of members) {
    if (rows.has(mailboxId)) continue
    rows.set(mailboxId, {
      mailbox_id: mailboxId,
      email: member.email,
      provider: member.provider,
      status: member.status,
      included: true,
      weight: String(member.weight),
      enabled: member.enabled,
      assignedCount: member.assigned_count,
      lastAssignedAt: member.last_assigned_at ?? null,
    })
  }

  // Sorted by address, not by membership: editing a weight or excluding a
  // mailbox must not make the rows jump around under the cursor.
  return [...rows.values()].sort((a, b) => a.email.localeCompare(b.email))
}

/**
 * Validates the rows and converts them to the request payload, returning the
 * problem instead when they can't be saved — the editor explains it inline
 * rather than bouncing the operator off the API's 422.
 *
 * Mirrors the server's rules deliberately: the API (and the `weight BETWEEN 1
 * AND 100` check constraint) remain the authority, this is just the fast,
 * specific feedback.
 */
export function fromDraft(
  rotationMode: RotationMode,
  rows: DraftSender[],
): { pool: CampaignSenderPoolRequest } | { problem: string } {
  const senders: CampaignSenderPoolRequest['senders'] = []

  for (const row of rows) {
    if (!row.included) continue
    const weight = parseWeight(row.weight)
    if (weight === null) {
      return { problem: `${row.email}: weight must be a whole number from 1 to 100.` }
    }
    senders.push({ mailbox_id: row.mailbox_id, weight, enabled: row.enabled })
  }

  if (senders.length === 0) {
    return { problem: 'Include at least one mailbox — a campaign with no senders can never send.' }
  }
  return { pool: { rotation_mode: rotationMode, senders } }
}

/** Strict integer parse in 1..100; `null` when the field can't be saved as-is. */
function parseWeight(raw: string): number | null {
  if (!/^\d+$/.test(raw.trim())) return null
  const weight = Number(raw.trim())
  if (weight < 1 || weight > 100) return null
  return weight
}

/**
 * Maps a pool-save failure to human copy, mirroring the API's 422 reasons and
 * preferring the server's own explanation when it sent one — the 422 covers
 * several distinct causes (inactive mailbox, foreign mailbox, bad weight) and
 * only the server knows which one fired.
 */
export function senderErrorMessage(error: unknown): string {
  const status = httpStatus(error)
  const reason = serverReason(error)
  if (status === 422) {
    return reason
      ? `That sender pool was rejected: ${reason}`
      : 'That sender pool was rejected — include at least one active workspace mailbox, each with a weight from 1 to 100.'
  }
  if (status === 404) return 'This campaign no longer exists.'
  return "Couldn't save the senders. Please try again."
}

/** The `{"error": "…"}` envelope the API writes, read through the typed seam. */
function serverReason(error: unknown): string | undefined {
  if (!isFetchBaseQueryError(error)) return undefined
  const reason = (error.data as { error?: string } | undefined)?.error
  return typeof reason === 'string' && reason.trim() !== '' ? reason : undefined
}
