import { useId, useState } from 'react'
import { Building2, ChartNoAxesColumn, ChevronRight, CircleCheckBig, CircleDollarSign, Inbox, SendHorizontal, LayoutDashboard, Mail, Megaphone, Users, Settings, Flame, Gauge, Sparkles, BookOpen, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { config } from '@/lib/config'
import { Button } from '@/components/ui/button'
import { NavLink } from '@/components/shared/nav-link'
import { PulseCard } from './pulse-card'
import { SidebarFooter } from './sidebar-footer'
import { useNavCounts } from './use-nav-counts'

/**
 * Primary navigation.
 *
 * One flat list of the screens a founder or marketer lives in, ordered by the
 * daily loop — check in, run campaigns, answer replies, manage the people and
 * the senders behind them — with Settings last. Everything else stays fully
 * reachable behind one collapsed "More" row.
 *
 * The previous nav showed all fourteen rows in five labeled groups at once;
 * user feedback was that the whole product surface shouting simultaneously
 * reads as overwhelming. Peer tools in this space keep the rail to a handful
 * of flat items, and that is the model here. No route was removed — the
 * secondary rows only moved behind a disclosure.
 *
 * Counts come from `useNavCounts` and are all real; a nav row with nothing
 * truthful to show simply has no count (see that hook for why some rows don't).
 */
/**
 * A row is either an in-app route (`to`) or an external link (`href`, opens a
 * new tab) — never both. Docs are the only external row today: the manuals are
 * the Astro/Starlight site under docs/, not an SPA page.
 */
type NavItem = { label: string; icon: LucideIcon } & (
  | {
      to: string
      href?: never
      /**
       * Highlight this row only on its exact path — needed for `/app`, the
       * parent of every screen in the product. Omitted rows keep prefix
       * matching, so Campaigns stays lit on a campaign's detail tabs. See
       * `components/shared/nav-link.tsx` for why this decision is explicit.
       */
      exact?: boolean
    }
  | { href: string; to?: never; exact?: never }
)

const PRIMARY: NavItem[] = [
  { label: 'Overview', to: '/app', icon: LayoutDashboard, exact: true },
  { label: 'Campaigns', to: '/app/campaigns', icon: Megaphone },
  { label: 'Inbox', to: '/app/inbox', icon: Inbox },
  { label: 'Contacts', to: '/app/contacts', icon: Users },
  { label: 'Mailboxes', to: '/app/mailboxes', icon: Mail },
  { label: 'Warmup', to: '/app/warmup', icon: Flame },
  { label: 'Reports', to: '/app/reports', icon: ChartNoAxesColumn },
  { label: 'Settings', to: '/app/settings', icon: Settings },
]

// The quieter screens: review queues, sending health detail, the rest of the
// CRM, and the external docs. All still one click away — just not shouting.
const MORE: NavItem[] = [
  { label: 'Approvals', to: '/app/approvals', icon: CircleCheckBig },
  { label: 'Outbox', to: '/app/outbox', icon: SendHorizontal },
  { label: 'Deliverability', to: '/app/deliverability', icon: Gauge },
  { label: 'Companies', to: '/app/companies', icon: Building2 },
  { label: 'Deals', to: '/app/deals', icon: CircleDollarSign },
  { label: 'Docs & MCP', href: config.docsUrl, icon: BookOpen },
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
        <span className="ml-auto rounded-md bg-chrome-surface px-1.5 py-0.5 font-mono text-[11px] tabular-nums text-chrome-muted">{count}</span>
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
    <NavLink
      to={item.to}
      exact={item.exact ?? false}
      className={rowClass}
      activeClassName="bg-chrome-hover font-medium text-chrome-text shadow-[inset_0_0_0_1px_var(--chrome-border)] before:absolute before:left-0 before:h-4 before:w-0.5 before:rounded-full before:bg-primary"
    >
      {content}
    </NavLink>
  )
}

/** Routes that live behind the "More" disclosure — used to open it on load
 * when the current page is one of them, so the active row is never hidden. */
const MORE_ROUTES = MORE.flatMap((item) => (item.to !== undefined ? [item.to] : []))

export function AppSidebar({ onOpenAgent = noop }: { onOpenAgent?: () => void }) {
  const counts = useNavCounts()
  const [moreOpen, setMoreOpen] = useState(() =>
    MORE_ROUTES.some((route) => window.location.pathname.startsWith(route)),
  )
  // The shell mounts the sidebar twice (desktop rail + mobile drawer), so the
  // disclosure target id must be unique per instance.
  const moreId = useId()

  // overflow-x-hidden: a row that fails to truncate must clip, never hand the
  // whole rail a horizontal scrollbar (overflow-y alone makes overflow-x auto).
  return (
    <div className="flex h-full w-64 flex-col overflow-x-hidden overflow-y-auto bg-chrome px-3 pb-3 pt-4">
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
        <kbd className="ml-auto rounded border border-chrome/30 px-1.5 py-0.5 font-mono text-[10px] text-chrome/70">@</kbd>
      </Button>
      <nav aria-label="Primary" className="flex flex-col gap-0.5">
        {PRIMARY.map((item) => (
          <NavRow key={item.to ?? item.href} item={item} count={item.to !== undefined ? counts[item.to] : undefined} />
        ))}

        <button
          type="button"
          onClick={() => setMoreOpen((open) => !open)}
          aria-expanded={moreOpen}
          aria-controls={moreId}
          className="group mt-2 flex h-9 cursor-pointer items-center gap-2.5 rounded-lg px-2.5 text-[13px] text-chrome-muted transition-colors hover:bg-chrome-hover hover:text-chrome-text"
        >
          <ChevronRight
            className={cn('size-4 shrink-0 transition-transform', moreOpen && 'rotate-90')}
            strokeWidth={1.75}
            aria-hidden="true"
          />
          <span>More</span>
        </button>
        {moreOpen && (
          <div id={moreId} className="flex flex-col gap-0.5">
            {MORE.map((item) => (
              <NavRow key={item.to ?? item.href} item={item} count={item.to !== undefined ? counts[item.to] : undefined} />
            ))}
          </div>
        )}
      </nav>
      <SidebarFooter />
    </div>
  )
}
