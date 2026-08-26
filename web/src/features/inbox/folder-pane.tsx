import { useMemo, useState } from 'react'
import {
  Inbox,
  MailOpen,
  CalendarDays,
  CalendarRange,
  Reply,
  BellOff,
  AtSign,
  ChevronDown,
  AlertCircle,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { httpStatus } from '@/lib/rtk-error'
import type { InboxOverview, InboxLabel } from './api'
import { SCOPE_LABELS, type InboxScope } from './inbox-search'

/**
 * The inbox's folder pane, structured the way a desktop mail client's is:
 * a Favorites section for the folders an operator lives in, the calendar and
 * snooze folders, then one row per connected mailbox (the "accounts"), then
 * the label taxonomy as categories. Every section collapses independently —
 * the collapse is view state for this visit, deliberately not persisted.
 */
const SCOPE_ICONS: Record<InboxScope, typeof Inbox> = {
  all: Inbox,
  unread: MailOpen,
  today: CalendarDays,
  this_week: CalendarRange,
  awaiting_reply: Reply,
  snoozed: BellOff,
}

/**
 * The pane's sections split the scopes by what they are: the piles an operator
 * triages from (favorites) versus the time/parking views. The union of the two
 * must stay exactly INBOX_SCOPES — nothing may become unreachable.
 */
const FAVORITE_SCOPES: readonly InboxScope[] = ['all', 'unread', 'awaiting_reply']
const FOLDER_SCOPES: readonly InboxScope[] = ['today', 'this_week', 'snoozed']

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
    case 'snoozed':
      return overview.snoozed
  }
}

export interface MailboxOption {
  id: string
  label: string
}

export function FolderPane({
  overview,
  overviewError,
  scope,
  mailboxes,
  mailboxesError,
  selectedMailbox,
  labels,
  selectedLabel,
  onSelectScope,
  onSelectMailbox,
  onSelectLabel,
}: {
  overview: InboxOverview | undefined
  overviewError: unknown
  scope: InboxScope
  mailboxes: readonly MailboxOption[]
  mailboxesError: unknown
  selectedMailbox: string
  labels: readonly InboxLabel[]
  selectedLabel: string
  onSelectScope: (scope: InboxScope) => void
  onSelectMailbox: (mailboxId: string) => void
  onSelectLabel: (labelId: string) => void
}) {
  // Per-mailbox counts are looked up by id: the API omits mailboxes holding no
  // threads, so an absent entry legitimately means zero (see InboxMailboxCount).
  const countByMailbox = useMemo(
    () => new Map((overview?.by_mailbox ?? []).map((m) => [m.mailbox_id, m])),
    [overview],
  )

  const scopeActive = (s: InboxScope) => scope === s && selectedMailbox === '' && selectedLabel === ''

  return (
    <nav
      aria-label="Inbox folders"
      className="flex max-h-48 w-full shrink-0 flex-col gap-1 overflow-y-auto border-b border-border bg-rail py-2 lg:max-h-none lg:w-56 lg:border-b-0 lg:border-r"
    >
      {overviewError !== undefined && (
        <p role="status" className="flex items-start gap-1.5 px-4 py-1 text-[11px] text-warn">
          <AlertCircle className="mt-px size-3 shrink-0" aria-hidden="true" />
          <span>Counts unavailable{httpStatus(overviewError) ? ` (${httpStatus(overviewError)})` : ''}.</span>
        </p>
      )}

      <FolderSection title="Favorites">
        {FAVORITE_SCOPES.map((s) => (
          <FolderRow
            key={s}
            icon={SCOPE_ICONS[s]}
            label={SCOPE_LABELS[s]}
            count={countForScope(s, overview)}
            active={scopeActive(s)}
            onSelect={() => onSelectScope(s)}
          />
        ))}
      </FolderSection>

      <FolderSection title="Folders">
        {FOLDER_SCOPES.map((s) => (
          <FolderRow
            key={s}
            icon={SCOPE_ICONS[s]}
            label={SCOPE_LABELS[s]}
            count={countForScope(s, overview)}
            active={scopeActive(s)}
            onSelect={() => onSelectScope(s)}
          />
        ))}
      </FolderSection>

      {mailboxesError !== undefined ? (
        <p role="alert" className="px-4 py-2 text-[11px] text-danger">
          Couldn't load mailboxes{httpStatus(mailboxesError) ? ` (${httpStatus(mailboxesError)})` : ''}.
        </p>
      ) : (
        mailboxes.length > 0 && (
          <FolderSection title="Mailboxes">
            {mailboxes.map((m) => {
              const counts = countByMailbox.get(m.id)
              return (
                <FolderRow
                  key={m.id}
                  icon={AtSign}
                  label={m.label}
                  // Absent from the breakdown means zero — but only once the
                  // overview has actually loaded. Before that the count is
                  // unknown, and "0" would be a claim we cannot make.
                  count={counts?.total ?? (overview ? 0 : undefined)}
                  unread={counts?.unread ?? 0}
                  active={selectedMailbox === m.id}
                  onSelect={() => onSelectMailbox(m.id)}
                />
              )
            })}
          </FolderSection>
        )
      )}

      {labels.length > 0 && (
        <FolderSection title="Categories">
          {labels.map((label) => (
            <FolderRow
              key={label.id}
              label={label.name}
              dotColor={label.color}
              // No count: the overview's breakdown is per reply-class, not per
              // operator label. Showing a number we do not have would mean
              // inventing one.
              count={undefined}
              active={selectedLabel === label.id}
              onSelect={() => onSelectLabel(label.id)}
            />
          ))}
        </FolderSection>
      )}
    </nav>
  )
}

/**
 * One collapsible section, Outlook-style: a small header row whose chevron
 * turns, hiding the rows beneath. Expanded by default — collapsing is an
 * explicit act of tidying, never the pane hiding folders on its own.
 */
function FolderSection({ title, children }: { title: string; children: React.ReactNode }) {
  const [collapsed, setCollapsed] = useState(false)
  return (
    <section>
      <button
        type="button"
        aria-expanded={!collapsed}
        onClick={() => setCollapsed((c) => !c)}
        className="flex w-full items-center gap-1 px-3 py-1 text-left text-[11px] font-semibold text-muted-foreground transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      >
        <ChevronDown
          className={cn('size-3 shrink-0 transition-transform', collapsed && '-rotate-90')}
          aria-hidden="true"
        />
        {title}
      </button>
      {!collapsed && (
        <ul className="grid grid-cols-2 sm:grid-cols-3 lg:block">{children}</ul>
      )}
    </section>
  )
}

function FolderRow({
  icon: Icon,
  label,
  count,
  unread = 0,
  dotColor,
  active,
  onSelect,
}: {
  icon?: typeof Inbox
  label: string
  count: number | undefined
  unread?: number
  /** A label's own colour, shown as a dot in place of an icon. */
  dotColor?: string
  active: boolean
  onSelect: () => void
}) {
  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        aria-current={active ? 'true' : undefined}
        className={cn(
          'mx-1.5 flex w-[calc(100%-0.75rem)] items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-[13px] text-muted-foreground transition-colors',
          'hover:bg-surface-2 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
          active && 'bg-accent font-semibold text-foreground',
        )}
      >
        {Icon && <Icon className={cn('size-3.5 shrink-0', active && 'text-accent-ink')} aria-hidden="true" />}
        {dotColor && (
          <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: dotColor }} aria-hidden="true" />
        )}
        <span className="min-w-0 flex-1 truncate">{label}</span>
        {/* Unread is the number an operator scans for, so it leads and carries
            the emphasis in the accent ink a mail client gives its unread
            counts; the total follows muted. A row with no unread shows only
            its total, never a "0" claim. */}
        {unread > 0 && (
          <span className="shrink-0 font-mono text-[11px] font-semibold tabular-nums text-accent-ink">
            {unread}
            <span className="sr-only"> unread</span>
          </span>
        )}
        {count !== undefined && (
          <span className="shrink-0 font-mono text-[10px] tabular-nums text-faint">{count}</span>
        )}
      </button>
    </li>
  )
}
