import { useListMailboxesQuery } from '@/features/mailboxes/api'
import { useListCampaignsQuery } from '@/features/campaigns/api'
import { useGetWarmupOverviewQuery } from '@/features/warmup/api'

/**
 * Live counts for the sidebar, keyed by nav route.
 *
 * The one place in `components/` that reads across features, and deliberately
 * isolated here so `AppSidebar` itself stays presentational. This follows the
 * codebase's existing rule: read-only reuse of another feature's *query hook* is
 * allowed (see `features/warmup/warmup-page.tsx` pulling the mailbox list),
 * cross-feature *UI* imports are not.
 *
 * Every number is real. The sidebar previously shipped no counts at all because
 * the design-spec era's placeholders were invented (`Deals 18`) — a wrong count
 * is worse than none, so a value only appears here if an endpoint actually
 * reports it, cheaply, for every user. Two rows therefore have no count:
 *
 * - **Contacts** — contacts are queried per list, so there is no workspace-wide
 *   total to show without inventing one or fanning out a query per list.
 * - **Team** — the only meaningful number is pending invites, and that endpoint
 *   is admin-only and needs the active workspace id. A nav count isn't worth a
 *   role-gated request that 403s for ordinary members.
 */
export function useNavCounts(): Record<string, number | undefined> {
  const { data: mailboxes } = useListMailboxesQuery()
  const { data: campaigns } = useListCampaignsQuery()
  const { data: warmup } = useGetWarmupOverviewQuery()

  return {
    '/app/mailboxes': mailboxes?.length,
    // Pool size, not "mailboxes eligible for warmup" — the count answers "how
    // many are actually exchanging mail".
    '/app/warmup': warmup?.pool_size,
    '/app/campaigns': campaigns?.length,
  }
}
