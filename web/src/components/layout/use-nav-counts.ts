import type { WorkspacePulse } from '@/features/pulse/api'
import { usePulseSelect } from './use-pulse'

/**
 * Live counts for the sidebar, keyed by nav route.
 *
 * All four numbers come from the one workspace pulse subscription
 * (`usePulseSelect`) — a single O(1) aggregate payload — replacing the old
 * full-list fetches that downloaded every mailbox/campaign just to print a
 * length. Contacts finally gets a real count because the pulse read-model
 * reports a workspace-wide total; previously no such aggregate existed.
 * The narrowed selector keeps the sidebar from re-rendering on poll ticks
 * whose counts are unchanged.
 *
 * The doctrine stands: every number is real, and a value only appears here if
 * an endpoint actually reports it, cheaply, for every user. Team still has no
 * count — the only meaningful number is pending invites, and that endpoint is
 * admin-only; a nav count isn't worth a role-gated request that 403s for
 * ordinary members.
 */
const selectNavCounts = (data: WorkspacePulse | undefined) => ({
  '/app/mailboxes': data?.mailboxes.total,
  // Pool size, not "mailboxes eligible for warmup" — the count answers "how
  // many are actually exchanging mail".
  '/app/warmup': data?.warmup.pool,
  '/app/campaigns': data?.campaigns.total,
  '/app/contacts': data?.contacts.total,
})

export function useNavCounts(): Record<string, number | undefined> {
  return usePulseSelect(selectNavCounts)
}
