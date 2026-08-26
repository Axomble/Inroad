import { Link } from '@tanstack/react-router'
import { Building2, ChartNoAxesColumn, CircleCheckBig, CircleDollarSign, Inbox, SendHorizontal, LayoutDashboard, Mail, Megaphone, Users, Settings, Flame, Gauge, Sparkles, BookOpen, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { config } from '@/lib/config'
import { Button } from '@/components/ui/button'
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
/**
 * A row is either an in-app route (`to`) or an external link (`href`, opens a
 * new tab) — never both. Docs are the only external row today: the manuals are
 * the Astro/Starlight site under docs/, not an SPA page.
 */
type NavItem = { label: string; icon: LucideIcon } & (
  | { to: string; href?: never }
  | { href: string; to?: never }
)

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
      // Beside the Inbox rather than under Sending: the outbox is where a
      // reply you just wrote waits, so it belongs with the surface you wrote it
      // from, not with campaign configuration.
      { label: 'Outbox', to: '/app/outbox', icon: SendHorizontal },
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
      // Sits under Outreach, not Sending: it ranks campaigns by the replies
      // they produced, where Deliverability is about the health of what sends.
      { label: 'Reports', to: '/app/reports', icon: ChartNoAxesColumn },
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
      { label: 'Docs & MCP', href: config.docsUrl, icon: BookOpen },
    ],
  },
]

const noop = () => undefined

function NavRow({ item, count }: { item: NavItem; count?: number }) {
  const Icon = item.icon
  const rowClass = cn(
    'group relative flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-[13px] text-chrome-muted transition-colors',
    'hover:bg-chrome-hover hover:text-chrome-text',
  )
  const content = (
    <>
      <Icon className="size-4 shrink-0" strokeWidth={1.75} aria-hidden="true" />
      <span className="truncate">{item.label}</span>
      {count != null && (
        // Right-aligned, tabular, and quiet — a reference number, not a badge
        // demanding action.
        <span className="ml-auto rounded-md bg-chrome-surface px-1.5 py-0.5 font-mono text-[10px] tabular-nums text-chrome-muted">{count}</span>
      )}
    </>
  )

  // External rows (docs) open in a new tab and can never be "active".
  if (item.href !== undefined) {
    return (
      <a href={item.href} target="_blank" rel="noreferrer" className={rowClass}>
        {content}
      </a>
    )
  }

  return (
    <Link
      to={item.to}
      className={rowClass}
      activeProps={{ className: 'bg-chrome-hover font-medium text-chrome-text shadow-[inset_0_0_0_1px_var(--chrome-border)] before:absolute before:left-0 before:h-4 before:w-0.5 before:rounded-full before:bg-primary' }}
    >
      {content}
    </Link>
  )
}

export function AppSidebar({ onOpenAgent = noop }: { onOpenAgent?: () => void }) {
  const counts = useNavCounts()

  return (
    <div className="flex h-full w-64 flex-col overflow-y-auto bg-chrome px-3 pb-3 pt-4">
      <PulseCard />
      {/* Inverse chrome, like the overview banner: near-black on the light
          theme, near-white on the dark one (chrome-text/chrome swap roles).
          Ghost variant as the base — the inverse fill replaces the tactile
          physics on purpose, so this reads as chrome, not as a form control. */}
      <Button
        variant="ghost"
        onClick={onOpenAgent}
        className="mb-4 h-9 w-full justify-start gap-2.5 rounded-lg bg-chrome-text px-2.5 text-[13px] font-medium text-chrome hover:bg-chrome-text/90 hover:text-chrome focus-visible:ring-primary"
      >
        <Sparkles className="size-4 shrink-0 text-primary" strokeWidth={1.75} aria-hidden="true" />
        <span>Agent</span>
        <kbd className="ml-auto rounded border border-chrome/30 px-1.5 py-0.5 font-mono text-[9px] text-chrome/70">@</kbd>
      </Button>
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
                // Counts are keyed by route; external rows (href) have none.
                <NavRow
                  key={item.to ?? item.href}
                  item={item}
                  count={item.to !== undefined ? counts[item.to] : undefined}
                />
              ))}
            </div>
          )
        })}
      </nav>
      <SidebarFooter />
    </div>
  )
}
