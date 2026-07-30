import { Link } from '@tanstack/react-router'
import { Mail, Megaphone, Users, Settings, Flame, ShieldCheck, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useNavCounts } from './use-nav-counts'

/**
 * Primary navigation.
 *
 * Only routes that actually exist ship here — placeholder rows with invented
 * counts belong to the design-spec era and would 404 in the real router. Add
 * rows back as the features they navigate to actually land.
 *
 * Grouped, per `docs/frontend-design.md` §4: sections encode the two things this
 * product does — keep mailboxes healthy enough to send (SENDING), then run
 * outreach through them (OUTREACH) — with workspace administration last. The
 * order is the operator's actual workflow, so the nav doubles as a sequence.
 *
 * Counts come from `useNavCounts` and are all real; a nav row with nothing
 * truthful to show simply has no count (see that hook for why Contacts doesn't).
 */
interface NavItem {
  label: string
  to: string
  icon: LucideIcon
}

interface NavGroup {
  /** Omitted for the first group so the nav doesn't open with a label. */
  label?: string
  items: NavItem[]
}

const NAV: NavGroup[] = [
  // No "Overview" row: `/app` only redirects to `/app/mailboxes`, so it would
  // duplicate the Mailboxes item. Add a real dashboard row once one exists.
  {
    label: 'Sending',
    items: [
      { label: 'Mailboxes', to: '/app/mailboxes', icon: Mail },
      { label: 'Warmup', to: '/app/warmup', icon: Flame },
    ],
  },
  {
    label: 'Outreach',
    items: [
      { label: 'Campaigns', to: '/app/campaigns', icon: Megaphone },
      { label: 'Contacts', to: '/app/contacts', icon: Users },
    ],
  },
  {
    label: 'Workspace',
    items: [
      { label: 'Team', to: '/app/settings/team', icon: Settings },
      { label: 'Security', to: '/app/settings/security', icon: ShieldCheck },
    ],
  },
]

function NavRow({ item, count }: { item: NavItem; count?: number }) {
  const Icon = item.icon
  return (
    <Link
      to={item.to}
      className={cn(
        'group flex h-7 items-center gap-2.5 rounded-md px-2 text-[12.5px] text-muted-foreground transition-colors',
        'hover:bg-surface-2 hover:text-foreground',
      )}
      activeProps={{ className: 'bg-surface-2 font-medium text-foreground' }}
    >
      <Icon className="size-4 shrink-0" strokeWidth={1.75} aria-hidden="true" />
      <span className="truncate">{item.label}</span>
      {count != null && (
        // Right-aligned, tabular, and quiet — a reference number, not a badge
        // demanding action.
        <span className="ml-auto font-mono text-[11px] tabular-nums text-faint">{count}</span>
      )}
    </Link>
  )
}

export function AppSidebar() {
  const counts = useNavCounts()

  return (
    <nav aria-label="Primary" className="flex h-full w-64 flex-col gap-4 overflow-y-auto px-3 py-4">
      {NAV.map((group, index) => (
        <div key={group.label ?? index} className="flex flex-col gap-0.5">
          {group.label && (
            <div className="px-2 pb-1 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
              {group.label}
            </div>
          )}
          {group.items.map((item) => (
            <NavRow key={item.to} item={item} count={counts[item.to]} />
          ))}
        </div>
      ))}
    </nav>
  )
}
