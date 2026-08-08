// Mailboxes, grouped by the domain they send from.
//
// Domain authentication is a property of a domain, not of a mailbox, but the
// operator reads this page one mailbox at a time. Rendering the DNS verdict as
// its own stacked section above the list meant a workspace with ten domains got
// ten paragraph-sized blocks before the first mailbox row; grouping instead puts
// each verdict on one line directly above the mailboxes it governs, so the cost
// of another domain is a row, not a screen.
import type { Mailbox, SendingDomain } from '@/store/api'

export interface MailboxDomainGroup {
  /** The sending domain, lowercased. Empty when an address has no `@`. */
  domain: string
  /**
   * This domain's DNS verdict. Absent while the domains query is in flight, and
   * also when the list doesn't cover the domain (a stale cache, or a mailbox
   * connected seconds ago) — the group still renders, just without a verdict.
   */
  auth?: SendingDomain
  /** In the order the caller supplied, so the page's own sort still governs. */
  mailboxes: Mailbox[]
  /**
   * Position of this group's first mailbox in the flattened render order.
   * `useListKeyboardNav` addresses rows by a single index, so groups have to
   * hand their rows offsets that stay continuous across group boundaries.
   */
  startIndex: number
}

/** The domain an address sends from: everything after the last `@`, lowercased. */
export function mailboxDomain(email: string | null | undefined): string {
  const address = email ?? ''
  const at = address.lastIndexOf('@')
  return at === -1 ? '' : address.slice(at + 1).toLowerCase()
}

/** What a group is called on screen. Never renders an empty heading. */
export function domainGroupLabel(group: MailboxDomainGroup): string {
  return group.domain || 'Unknown domain'
}

/**
 * Sort key for a group. A domain that needs a DNS record outranks everything,
 * because it is the one problem on this page that no amount of warmup or
 * rotation works around. An unverified domain (`unknown`, or no verdict yet) is
 * ranked below a healthy domain holding a broken mailbox: "we couldn't look it
 * up" is not a fault, and must not outrank one.
 */
function groupRank(group: MailboxDomainGroup): number {
  if (group.auth?.state === 'failing') return 0
  if (group.mailboxes.some((mailbox) => mailbox.status === 'error')) return 1
  if (!group.auth || group.auth.state === 'unknown') return 2
  return 3
}

/**
 * Groups mailboxes by sending domain, worst-first, and stamps each group with
 * the render offset of its first row.
 *
 * Total mailbox count is preserved: every input lands in exactly one group, so
 * a caller can keep using `mailboxes.length` as its keyboard-nav row count.
 */
export function groupMailboxesByDomain(
  mailboxes: readonly Mailbox[],
  domains: readonly SendingDomain[] = [],
): MailboxDomainGroup[] {
  const authByDomain = new Map(domains.map((domain) => [domain.domain.toLowerCase(), domain]))

  // Insertion-ordered buckets; the sort below is the only thing that reorders.
  const buckets = new Map<string, Mailbox[]>()
  for (const mailbox of mailboxes) {
    const domain = mailboxDomain(mailbox.email)
    const bucket = buckets.get(domain)
    if (bucket) bucket.push(mailbox)
    else buckets.set(domain, [mailbox])
  }

  const groups: MailboxDomainGroup[] = [...buckets].map(([domain, grouped]) => ({
    domain,
    auth: authByDomain.get(domain),
    mailboxes: grouped,
    startIndex: 0,
  }))

  groups.sort((a, b) => groupRank(a) - groupRank(b) || a.domain.localeCompare(b.domain))

  let cursor = 0
  for (const group of groups) {
    group.startIndex = cursor
    cursor += group.mailboxes.length
  }
  return groups
}
