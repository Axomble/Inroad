import { Link } from '@tanstack/react-router'
import { Mail, Megaphone, Users, LayoutDashboard, Settings, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * Primary navigation. Only routes that actually exist ship here — placeholder
 * items with fake counts (deals: 18, mailboxes: 12, ...) belong to the
 * design-spec era and would 404 in the real router. Add rows back as the
 * features they navigate to actually land.
 */
interface NavItem {
  label: string
  to: string
  icon: LucideIcon
}

const NAV: NavItem[] = [
  { label: 'Overview', to: '/app', icon: LayoutDashboard },
  { label: 'Mailboxes', to: '/app/mailboxes', icon: Mail },
  { label: 'Campaigns', to: '/app/campaigns', icon: Megaphone },
  { label: 'Contacts', to: '/app/contacts', icon: Users },
  { label: 'Team', to: '/app/settings/team', icon: Settings },
]

function NavRow({ item }: { item: NavItem }) {
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
    </Link>
  )
}

export function AppSidebar() {
  return (
    <nav aria-label="Primary" className="flex h-full w-64 flex-col gap-4 overflow-y-auto px-3 py-4">
      <div className="flex flex-col gap-0.5">
        {NAV.map((item) => (
          <NavRow key={item.to} item={item} />
        ))}
      </div>
    </nav>
  )
}
