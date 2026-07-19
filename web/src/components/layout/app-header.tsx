import { Menu, Search, Bell } from 'lucide-react'
import { useNavigate } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
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
import { useAuthLogoutAllMutation, useAuthLogoutMutation } from '@/features/auth/api'
import { WorkspaceSwitcher } from '@/features/auth/workspace-switcher'

export function AppHeader({ onToggleNav }: { onToggleNav: () => void }) {
  const role = useAppSelector((state) => state.auth.role)
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
    navigate({ to: '/' })
  }

  async function handleLogoutAll() {
    try {
      await logoutAll().unwrap()
    } catch {
      // ignore — session is cleared below either way
    }
    dispatch(clearSession())
    navigate({ to: '/' })
  }

  return (
    <header className="flex h-14 shrink-0 items-center gap-3 border-b border-border bg-rail px-4">
      <Button
        variant="ghost"
        size="icon-sm"
        className="md:hidden"
        onClick={onToggleNav}
        aria-label="Toggle navigation"
      >
        <Menu />
      </Button>

      <div className="flex items-center gap-2">
        <div className="grid size-7 place-items-center rounded-md bg-primary text-sm font-bold text-primary-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.25)]">
          I
        </div>
        <span className="text-[15px] font-bold tracking-tight">Inroad</span>
      </div>

      <div className="hidden items-center gap-1 border-l border-border pl-3 sm:flex">
        <WorkspaceSwitcher />
      </div>

      <div className="ml-auto flex items-center gap-2">
        <Button variant="chip" size="chip" className="gap-2 text-muted-foreground">
          <Search />
          <span className="hidden sm:inline">Search</span>
          <kbd className="hidden rounded border border-border-strong bg-background/40 px-1 font-mono text-[10px] sm:inline">
            ⌘K
          </kbd>
        </Button>

        <Button variant="ghost" size="icon-sm" aria-label="Notifications">
          <Bell />
        </Button>

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              className="rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring"
              aria-label="Account menu"
            >
              <Avatar>
                <AvatarFallback>AO</AvatarFallback>
              </Avatar>
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-52">
            <DropdownMenuLabel>{role ? `${role.charAt(0).toUpperCase()}${role.slice(1)}` : 'Account'}</DropdownMenuLabel>
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
    </header>
  )
}
