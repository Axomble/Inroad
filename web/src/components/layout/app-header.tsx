import { Menu, Search, Bell } from 'lucide-react'
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

export function AppHeader({ onToggleNav }: { onToggleNav: () => void }) {
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
            <DropdownMenuLabel>Acme Owner</DropdownMenuLabel>
            <DropdownMenuItem>Profile</DropdownMenuItem>
            <DropdownMenuItem>Workspace settings</DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem variant="destructive">Log out</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  )
}
