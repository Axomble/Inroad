import { useNavigate } from '@tanstack/react-router'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import { clearSession } from '@/store/slices/auth'
import { useAuthLogoutAllMutation, useAuthLogoutMutation } from './api'
import { WorkspaceSwitcher } from './workspace-switcher'

/**
 * Auth-owned header slot: workspace switcher + account menu (profile,
 * workspace settings, logout / logout everywhere). Rendered by the app
 * layout via the `rightSlot` prop on `AppHeader`, so `AppHeader` (a layout
 * component) no longer needs to import from features/* — restoring the
 * layout -> feature layering direction.
 */
function initialsFromIdentity(name: string | null, email: string | null): string {
  const source = name?.trim() || email?.trim() || ''
  if (!source) return '?'
  const parts = source.split(/[\s@._-]+/).filter(Boolean)
  const first = parts[0]?.[0] ?? ''
  const second = parts.length > 1 ? (parts[1][0] ?? '') : ''
  const letters = (first + second).toUpperCase()
  return letters || source[0]!.toUpperCase()
}

export function AuthHeader() {
  const role = useAppSelector((state) => state.auth.role)
  const userName = useAppSelector((state) => state.auth.userName)
  const userEmail = useAppSelector((state) => state.auth.userEmail)
  const dispatch = useAppDispatch()
  const navigate = useNavigate()
  const [logout] = useAuthLogoutMutation()
  const [logoutAll] = useAuthLogoutAllMutation()

  // Regardless of whether the server call succeeds, drop the in-memory
  // session and send the user back to the marketing/login route — a failed
  // network call is not a reason to leave the SPA looking authenticated.
  async function handleLogout() {
    try {
      await logout().unwrap()
    } catch {
      // ignore — session is cleared below either way
    }
    dispatch(clearSession())
    void navigate({ to: '/' })
  }

  async function handleLogoutAll() {
    try {
      await logoutAll().unwrap()
    } catch {
      // ignore — session is cleared below either way
    }
    dispatch(clearSession())
    void navigate({ to: '/' })
  }

  const initials = initialsFromIdentity(userName, userEmail)

  return (
    <>
      <div className="hidden items-center gap-1 border-l border-border pl-3 sm:flex">
        <WorkspaceSwitcher />
      </div>

      <div className="ml-auto flex items-center gap-2">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              className="rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring"
              aria-label="Account menu"
            >
              <Avatar>
                <AvatarFallback>{initials}</AvatarFallback>
              </Avatar>
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-52">
            <DropdownMenuLabel>
              {userName || userEmail || (role ? `${role.charAt(0).toUpperCase()}${role.slice(1)}` : 'Account')}
            </DropdownMenuLabel>
            <DropdownMenuItem>Profile</DropdownMenuItem>
            <DropdownMenuItem>Workspace settings</DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive" onSelect={() => void handleLogout()}>
              Log out
            </DropdownMenuItem>
            <DropdownMenuItem variant="destructive" onSelect={() => void handleLogoutAll()}>
              Log out everywhere
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </>
  )
}
