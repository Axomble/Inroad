import { Link } from '@tanstack/react-router'
import { Building2, CircleCheckBig, CircleDollarSign, Inbox, LayoutDashboard, Mail, Megaphone, Users, Settings, Flame, Gauge, Sparkles, BookOpen, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { PulseCard } from './pulse-card'
import { SidebarFooter } from './sidebar-footer'
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
  {
    items: [
      { label: 'Overview', to: '/app', icon: LayoutDashboard },
      { label: 'Approvals', to: '/app/approvals', icon: CircleCheckBig },
      { label: 'Inbox', to: '/app/inbox', icon: Inbox },
    ],
  },
  {
    label: 'Sending',
    items: [
      { label: 'Mailboxes', to: '/app/mailboxes', icon: Mail },
      { label: 'Warmup', to: '/app/warmup', icon: Flame },
      // Sits with the sending mailboxes rather than under Outreach: the score is
      // about the health of what sends, not about any one campaign.
      { label: 'Deliverability', to: '/app/deliverability', icon: Gauge },
    ],
  },
  {
    label: 'Outreach',
    items: [
      // Campaigns is outbound sequencing, not a CRM record type — it stays here
      // even though the people it mails are CRM contacts.
      { label: 'Campaigns', to: '/app/campaigns', icon: Megaphone },
    ],
  },
  {
    // The three CRM record types, in the order a deal is built: a person, the
    // account they belong to, the opportunity that comes out of it. Each appears
    // exactly once — the old nav listed Deals twice (its own row plus a "CRM"
    // row whose page opened on a deals tab) and left Contacts outside the CRM
    // it is part of.
    label: 'CRM',
    items: [
      { label: 'Contacts', to: '/app/contacts', icon: Users },
      { label: 'Companies', to: '/app/companies', icon: Building2 },
      { label: 'Deals', to: '/app/deals', icon: CircleDollarSign },
    ],
  },
  {
    // Seven settings screens used to sit here as seven top-level rows, giving
    // workspace administration more of the primary nav than Inbox, Campaigns
    // and the whole CRM combined. They now live behind one row, on the
    // settings rail (`components/layout/settings-rail.tsx`) — which is also
    // where the next settings screen goes, instead of here.
    label: 'Workspace',
    items: [
      { label: 'Settings', to: '/app/settings', icon: Settings },
      // Stays top-level: it documents the API and MCP server for people
      // integrating with Inroad, which is not workspace administration.
      { label: 'Docs & MCP', to: '/app/docs', icon: BookOpen },
    ],
  },
]

const noop = () => undefined

function NavRow({ item, count }: { item: NavItem; count?: number }) {
  const Icon = item.icon
  return (
    <Link
      to={item.to}
      className={cn(
        'group relative flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-[13px] text-chrome-muted transition-colors',
        'hover:bg-chrome-hover hover:text-chrome-text',
      )}
      activeProps={{ className: 'bg-chrome-hover font-medium text-chrome-text shadow-[inset_0_0_0_1px_var(--chrome-border)] before:absolute before:left-0 before:h-4 before:w-0.5 before:rounded-full before:bg-primary' }}
    >
      <Icon className="size-4 shrink-0" strokeWidth={1.75} aria-hidden="true" />
      <span className="truncate">{item.label}</span>
      {count != null && (
        // Right-aligned, tabular, and quiet — a reference number, not a badge
        // demanding action.
        <span className="ml-auto rounded-md bg-chrome-surface px-1.5 py-0.5 font-mono text-[10px] tabular-nums text-chrome-muted">{count}</span>
      )}
    </Link>
  )
}

export function AppSidebar({ onOpenAgent = noop }: { onOpenAgent?: () => void }) {
  const counts = useNavCounts()

  return (
    <div className="flex h-full w-64 flex-col overflow-y-auto bg-chrome px-3 pb-3 pt-4">
      <PulseCard />
      <button
        type="button"
        onClick={onOpenAgent}
        className="mb-4 flex h-9 items-center gap-2.5 rounded-lg border border-primary/25 bg-primary/10 px-2.5 text-[13px] font-medium text-chrome-text transition-colors hover:border-primary/45 hover:bg-primary/15 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
      >
        <Sparkles className="size-4 shrink-0 text-primary" strokeWidth={1.75} aria-hidden="true" />
        <span>Ask Inroad</span>
        <kbd className="ml-auto rounded border border-chrome-border px-1.5 py-0.5 font-mono text-[9px] text-chrome-muted">@</kbd>
      </button>
      <nav aria-label="Primary" className="flex flex-col gap-5">
        {NAV.map((group, index) => {
          return (
            <div key={group.label ?? index} className="flex flex-col gap-0.5">
              {group.label && (
                <div className="px-2.5 pb-1 font-mono text-[9px] font-medium uppercase tracking-[0.18em] text-chrome-muted/70">
                  {group.label}
                </div>
              )}
              {group.items.map((item) => (
                <NavRow key={item.to} item={item} count={counts[item.to]} />
              ))}
            </div>
          )
        })}
      </nav>
      <SidebarFooter />
    </div>
  )
}
