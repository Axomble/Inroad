import { Link } from '@tanstack/react-router'
import { AlertTriangle, KeyRound, ListPlus, Plug, Settings, ShieldCheck, Sparkles, Tags, type LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useHasRole } from '@/hooks/use-has-role'
import type { WorkspaceRole } from '@/lib/rbac'

/**
 * Second-level navigation for /app/settings/*.
 *
 * These screens used to be top-level sidebar rows, so workspace
 * administration occupied more of the primary nav than Inbox, Campaigns and
 * the whole CRM combined — and every new settings page made it worse. Moving
 * them one level down restores the sidebar to the operator's actual workflow
 * and gives settings somewhere to grow.
 *
 * The rail is vertical rather than a tab strip because each destination still
 * renders its own `<Page>` with its own topbar; a horizontal strip would sit
 * above seven different titles and read as a second, competing header.
 *
 * Below `sm` it becomes a horizontally scrollable row — a full vertical rail
 * would push the actual settings content off a phone screen.
 */
interface SettingsItem {
  label: string
  to: string
  icon: LucideIcon
  /**
   * The role this destination's routes declare via `auth.RequireRole` on the
   * server. Omitted where every member can act, so the field names the same
   * requirement the backend enforces instead of restating it as a boolean.
   * The route stays reachable and renders its own insufficient-role state on a
   * deep link — see `@/lib/rbac` for why this is courtesy, not authorization.
   */
  minRole?: WorkspaceRole
}

const SETTINGS_NAV: SettingsItem[] = [
  // identity/routes.go wraps every /workspaces/{id}/invites route in
  // RequireRole("admin"), so a member reaching this screen only ever sees its
  // "Admins only" state — the old sidebar listed it unconditionally anyway.
  { label: 'Team', to: '/app/settings/team', icon: Settings, minRole: 'admin' },
  // Personal account security (2FA, passkeys, active sessions): the one
  // settings screen every role can fully use, which is why the index lands here.
  { label: 'Security', to: '/app/settings/security', icon: ShieldCheck },
  // No minRole: reply-label writes are campaigns:write and custom-field writes
  // are contacts:write — scopes every logged-in member holds. Only the API-key,
  // OAuth-grant and AI surfaces sit behind RequireRole("admin").
  { label: 'Reply labels', to: '/app/settings/reply-labels', icon: Tags },
  { label: 'Custom fields', to: '/app/settings/custom-fields', icon: ListPlus },
  // apikey/routes.go, oauthprovider/routes.go and aisettings/routes.go each
  // wrap their router in auth.RequireRole("admin").
  { label: 'API keys', to: '/app/settings/api-keys', icon: KeyRound, minRole: 'admin' },
  { label: 'Connected apps', to: '/app/settings/oauth-apps', icon: Plug, minRole: 'admin' },
  { label: 'AI', to: '/app/settings/ai', icon: Sparkles, minRole: 'admin' },
  // No minRole: the list needs campaigns:read, which every member holds. Replay and
  // discard need campaigns:send and are refused by the server with a 403 the row
  // renders — the same courtesy-not-authorization split the rest of this table uses.
  { label: 'Failed tasks', to: '/app/settings/dead-letters', icon: AlertTriangle },
]

export function SettingsRail() {
  const isAdmin = useHasRole('admin')
  // Every gated row in this table happens to require 'admin'; reading the one
  // rank once keeps the hook call unconditional, as the rules require.
  const items = SETTINGS_NAV.filter((item) => item.minRole == null || isAdmin)

  return (
    <nav
      aria-label="Settings"
      data-slot="settings-rail"
      className={cn(
        'flex shrink-0 gap-0.5 border-border bg-surface/60 p-2',
        // Phone: a scrolling strip above the content. sm+: a fixed left rail.
        'overflow-x-auto border-b',
        'sm:w-52 sm:flex-col sm:overflow-x-visible sm:overflow-y-auto sm:border-b-0 sm:border-r sm:p-3',
      )}
    >
      <div className="hidden px-2.5 pb-2 font-mono text-[9px] font-medium uppercase tracking-[0.18em] text-faint sm:block">
        Settings
      </div>
      {items.map((item) => {
        const Icon = item.icon
        return (
          <Link
            key={item.to}
            to={item.to}
            data-slot="settings-rail-link"
            className={cn(
              'flex h-9 shrink-0 items-center gap-2.5 rounded-lg px-2.5 text-[13px] text-muted-foreground transition-colors',
              'hover:bg-surface-2 hover:text-foreground',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
            )}
            activeProps={{
              className: 'bg-surface-2 font-medium text-foreground shadow-[inset_0_0_0_1px_var(--border)]',
            }}
          >
            <Icon className="size-4 shrink-0" strokeWidth={1.75} aria-hidden="true" />
            <span className="truncate">{item.label}</span>
          </Link>
        )
      })}
    </nav>
  )
}
