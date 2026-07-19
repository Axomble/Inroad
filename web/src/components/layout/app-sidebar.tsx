import { Link } from '@tanstack/react-router'
import {
  Inbox,
  Mail,
  Megaphone,
  Users,
  BarChart3,
  ShieldCheck,
  GitBranch,
  CircleDollarSign,
  CheckSquare,
  Calendar,
  FileText,
  Plug,
  Workflow,
  KeyRound,
  ScrollText,
  Settings,
  type LucideIcon,
} from 'lucide-react'
import { cn } from '@/lib/utils'

interface NavItem {
  label: string
  to: string
  icon: LucideIcon
  /** Live count fed by RTK Query later; static placeholders for now. */
  count?: number
  /** Marks a warmup-related row so it can carry the reserved amber motif. */
  warm?: boolean
}

interface NavGroup {
  label?: string
  items: NavItem[]
}

const NAV: NavGroup[] = [
  { items: [{ label: 'Inbox', to: '/app/unibox', icon: Inbox, count: 3 }] },
  {
    label: 'Email',
    items: [
      { label: 'Mailboxes', to: '/app/mailboxes', icon: Mail, count: 12, warm: true },
      { label: 'Campaigns', to: '/app/campaigns', icon: Megaphone, count: 8 },
      { label: 'Contacts', to: '/app/contacts', icon: Users },
      { label: 'Analytics', to: '/app/analytics', icon: BarChart3 },
      { label: 'Deliverability', to: '/app/deliverability', icon: ShieldCheck },
    ],
  },
  {
    label: 'CRM',
    items: [
      { label: 'Pipelines', to: '/app/pipelines', icon: GitBranch },
      { label: 'Deals', to: '/app/deals', icon: CircleDollarSign, count: 18 },
      { label: 'Tasks', to: '/app/tasks', icon: CheckSquare },
      { label: 'Meetings', to: '/app/meetings', icon: Calendar },
    ],
  },
  {
    label: 'Resources',
    items: [
      { label: 'Templates', to: '/app/templates', icon: FileText },
      { label: 'Integrations', to: '/app/integrations', icon: Plug },
      { label: 'Automations', to: '/app/automations', icon: Workflow },
      { label: 'API Keys', to: '/app/api-keys', icon: KeyRound },
      { label: 'Audit log', to: '/app/audit', icon: ScrollText },
    ],
  },
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
      {item.count != null && (
        <span
          className={cn(
            'ml-auto font-mono text-[11px] tabular-nums',
            item.warm ? 'text-warm' : 'text-faint',
          )}
        >
          {item.count}
        </span>
      )}
    </Link>
  )
}

export function AppSidebar() {
  return (
    <nav aria-label="Primary" className="flex h-full w-64 flex-col gap-4 overflow-y-auto px-3 py-4">
      {NAV.map((group, i) => (
        <div key={group.label ?? i} className="flex flex-col gap-0.5">
          {group.label && (
            <div className="mb-1 px-2 font-mono text-[10px] uppercase tracking-[0.16em] text-faint">
              {group.label}
            </div>
          )}
          {group.items.map((item) => (
            <NavRow key={item.to} item={item} />
          ))}
        </div>
      ))}

      <div className="mt-auto flex flex-col gap-0.5 border-t border-border pt-3">
        <NavRow item={{ label: 'Settings', to: '/app/settings', icon: Settings }} />
      </div>
    </nav>
  )
}
