import { useMemo } from 'react'
import { Inbox, MailOpen, CalendarDays, CalendarRange, Reply, AlertCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { httpStatus } from '@/lib/rtk-error'
import type { InboxOverview } from './api'
import { INBOX_SCOPES, SCOPE_LABELS, type InboxScope } from './inbox-search'

/**
 * The rail's icon per scope. Separate from SCOPE_LABELS (which lives beside
 * the URL contract in inbox-search.ts) because an icon is presentation and
 * has no business in the module that defines the API contract.
 */
const SCOPE_ICONS: Record<InboxScope, typeof Inbox> = {
  all: Inbox,
  unread: MailOpen,
  today: CalendarDays,
  this_week: CalendarRange,
  awaiting_reply: Reply,
}

/**
 * Which of the overview's counters a scope displays. `all` shows the total;
 * every other scope shows its own count, so the number beside a folder is
 * always the number of threads that folder will list.
 */
function countForScope(scope: InboxScope, overview: InboxOverview | undefined): number | undefined {
  if (!overview) return undefined
  switch (scope) {
    case 'all':
      return overview.total
    case 'unread':
      return overview.unread
    case 'today':
      return overview.today
    case 'this_week':
      return overview.this_week
    case 'awaiting_reply':
      return overview.awaiting_reply
  }
}

export interface MailboxOption {
  id: string
  label: string
}

/**
 * The inbox's left pane: virtual folders on top, then one row per connected
 * mailbox.
 *
 * Counts come from `/inbox/overview` — counted by the database over the whole
 * workspace. Where the count is not loaded yet the row renders without one
 * rather than with a placeholder zero: "0" is a claim, and an unloaded count
 * has no business making it.
 */
export function ScopeRail({
  overview,
  overviewError,
  scope,
  mailboxes,
  mailboxesError,
  selectedMailbox,
  onSelectScope,
  onSelectMailbox,
}: {
  overview: InboxOverview | undefined
  overviewError: unknown
  scope: InboxScope
  mailboxes: readonly MailboxOption[]
  mailboxesError: unknown
  selectedMailbox: string
  onSelectScope: (scope: InboxScope) => void
  onSelectMailbox: (mailboxId: string) => void
}) {
  // Per-mailbox counts are looked up by id: the API omits mailboxes holding no
  // threads, so an absent entry legitimately means zero (see InboxMailboxCount).
  const countByMailbox = useMemo(
    () => new Map((overview?.by_mailbox ?? []).map((m) => [m.mailbox_id, m])),
    [overview],
  )

  return (
    <nav
      aria-label="Inbox folders"
      className="flex max-h-44 w-full shrink-0 flex-col overflow-y-auto border-b border-border lg:max-h-none lg:w-56 lg:border-b-0 lg:border-r"
    >
      {overviewError !== undefined && (
        <p role="status" className="flex items-start gap-1.5 px-4 py-2 text-[11px] text-warn">
          <AlertCircle className="mt-px size-3 shrink-0" aria-hidden="true" />
          <span>Counts unavailable{httpStatus(overviewError) ? ` (${httpStatus(overviewError)})` : ''}.</span>
        </p>
      )}

      <ul className="grid grid-cols-2 sm:grid-cols-3 lg:block">
        {INBOX_SCOPES.map((s) => (
          <li key={s}>
            <RailRow
              icon={SCOPE_ICONS[s]}
              label={SCOPE_LABELS[s]}
              count={countForScope(s, overview)}
              active={scope === s && selectedMailbox === ''}
              onSelect={() => onSelectScope(s)}
            />
          </li>
        ))}
      </ul>

      {mailboxesError !== undefined ? (
        <p role="alert" className="p-3 text-[11px] text-danger">
          Couldn't load mailboxes{httpStatus(mailboxesError) ? ` (${httpStatus(mailboxesError)})` : ''}.
        </p>
      ) : (
        mailboxes.length > 0 && (
          <>
            <p className="px-4 pt-3 pb-1 font-mono text-[10px] tracking-wide text-faint uppercase">Mailboxes</p>
            <ul className="grid grid-cols-2 sm:grid-cols-3 lg:block">
              {mailboxes.map((m) => {
                const counts = countByMailbox.get(m.id)
                return (
                  <li key={m.id}>
                    <RailRow
                      label={m.label}
                      // Absent from the breakdown means zero — but only once the
                      // overview has actually loaded. Before that the count is
                      // unknown, and "0" would be a claim we cannot make.
                      count={counts?.total ?? (overview ? 0 : undefined)}
                      unread={counts?.unread ?? 0}
                      active={selectedMailbox === m.id}
                      onSelect={() => onSelectMailbox(m.id)}
                    />
                  </li>
                )
              })}
            </ul>
          </>
        )
      )}
    </nav>
  )
}

function RailRow({
  icon: Icon,
  label,
  count,
  unread = 0,
  active,
  onSelect,
}: {
  icon?: typeof Inbox
  label: string
  count: number | undefined
  unread?: number
  active: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={active ? 'true' : undefined}
      className={cn(
        'flex w-full items-center gap-2 px-4 py-2 text-left text-[13px] text-muted-foreground transition-colors',
        'hover:bg-surface-2 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
        active && 'bg-surface-2 font-medium text-foreground',
      )}
    >
      {Icon && <Icon className="size-3.5 shrink-0" aria-hidden="true" />}
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {/* Unread is the number an operator scans for, so it leads and carries
          the emphasis; the total follows in the muted chip. A row with no
          unread shows only its total, not a "0". Scope rows pass no `unread`
          — for them the total already IS the number the label names (the
          Unread folder's total is its unread count), so a second emphasized
          copy of the same figure would only add noise. */}
      {unread > 0 && (
        <span className="shrink-0 font-mono text-[10px] font-semibold tabular-nums text-primary">
          {unread}
          <span className="sr-only"> unread</span>
        </span>
      )}
      {count !== undefined && (
        <span className="shrink-0 rounded-md bg-surface-2 px-1.5 py-0.5 font-mono text-[10px] tabular-nums text-muted-foreground">
          {count}
        </span>
      )}
    </button>
  )
}
