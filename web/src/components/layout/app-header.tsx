import { Menu, Search } from 'lucide-react'
import { Button } from '@/components/ui/button'

/**
 * App shell header. Deliberately feature-agnostic — the workspace switcher and
 * account menu live in `features/auth/auth-header.tsx` and are passed in via
 * the `rightSlot` prop. Layout components must not import from features/*,
 * which is the direction the layering rule mandates.
 */
export function AppHeader({
  navOpen,
  onToggleNav,
  onOpenPalette,
  rightSlot,
}: {
  onToggleNav: () => void
  onOpenPalette: () => void
  navOpen: boolean
  rightSlot?: React.ReactNode
}) {
  return (
    <header className="flex h-14 shrink-0 items-center gap-3 border-b border-chrome-border bg-chrome px-3 text-chrome-text sm:px-4">
      <Button
        variant="ghost"
        size="icon-sm"
        className="text-chrome-muted hover:bg-chrome-hover hover:text-chrome-text md:hidden"
        onClick={onToggleNav}
        aria-label="Toggle navigation"
        aria-expanded={navOpen}
        aria-controls="mobile-navigation"
      >
        <Menu />
      </Button>

      <div className="flex items-center gap-2.5">
        <div className="relative grid size-7 place-items-center rounded-lg bg-primary text-sm font-black text-primary-foreground shadow-[0_0_18px_rgba(195,245,60,0.28)]">
          <span className="relative z-10">I</span>
          <span className="absolute -right-0.5 -top-0.5 size-2 rounded-full border-2 border-chrome bg-data" aria-hidden="true" />
        </div>
        <div className="leading-none">
          <span className="text-[15px] font-bold tracking-[-0.025em]">Inroad</span>
          <span className="ml-2 hidden font-mono text-[8px] uppercase tracking-[0.18em] text-chrome-muted lg:inline">Outreach OS</span>
        </div>
      </div>

      <button
        type="button"
        onClick={onOpenPalette}
        className="ml-2 hidden h-8 w-full max-w-72 items-center gap-2 rounded-lg border border-chrome-border bg-chrome-surface px-2.5 text-left text-xs text-chrome-muted outline-none transition-colors hover:border-chrome-muted/40 hover:text-chrome-text focus-visible:ring-2 focus-visible:ring-primary sm:flex"
        aria-label="Open command palette"
      >
        <Search className="size-3.5" aria-hidden="true" />
        <span className="flex-1">Jump to anything</span>
        <kbd className="rounded border border-chrome-border px-1.5 py-0.5 font-mono text-[9px] text-chrome-muted">⌘ K</kbd>
      </button>

      {rightSlot}
    </header>
  )
}
