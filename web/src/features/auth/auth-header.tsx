import { Link, useNavigate } from '@tanstack/react-router'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Skeleton } from '@/components/ui/skeleton'
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
import { ThemeToggle } from '@/components/layout/theme-toggle'
import { initialsFromIdentity } from '@/lib/initials'

/**
 * Auth-owned header slot: workspace switcher + account menu (profile,
 * workspace settings, logout / logout everywhere). Rendered by the app
 * layout via the `rightSlot` prop on `AppHeader`, so `AppHeader` (a layout
 * component) no longer needs to import from features/* — restoring the
 * layout -> feature layering direction.
 */
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
      <div className="hidden items-center gap-1 border-l border-chrome-border pl-3 sm:flex">
        <WorkspaceSwitcher />
      </div>

      <div className="ml-auto flex items-center gap-2">
        <ThemeToggle />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              className="rounded-full outline-none ring-offset-2 ring-offset-chrome focus-visible:ring-2 focus-visible:ring-primary"
              aria-label="Account menu"
            >
              {/* A neutral skeleton until the session settles — reads as
                  "loading", never a wrong-looking identity flash. Same shape
                  as the avatar so nothing shifts when initials land. */}
              {initials ? (
                <Avatar>
                  <AvatarFallback>{initials}</AvatarFallback>
                </Avatar>
              ) : (
                <Skeleton className="size-8 bg-chrome-surface" />
              )}
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-52">
            <DropdownMenuLabel>
              {userName || userEmail || (role ? `${role.charAt(0).toUpperCase()}${role.slice(1)}` : 'Account')}
            </DropdownMenuLabel>
            {/*
              "Profile" used to sit here with no handler and no route — a menu
              item that does nothing teaches people not to trust the menu. It
              comes back when there's a profile screen to open. "Workspace
              settings" now goes where workspace settings actually live.
            */}
            <DropdownMenuItem asChild>
              <Link to="/app/settings/team">Workspace settings</Link>
            </DropdownMenuItem>
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
