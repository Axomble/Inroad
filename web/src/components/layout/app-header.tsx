import { Menu } from 'lucide-react'
import { Button } from '@/components/ui/button'

/**
 * App shell header. Deliberately feature-agnostic — the workspace switcher and
 * account menu live in `features/auth/auth-header.tsx` and are passed in via
 * the `rightSlot` prop. Layout components must not import from features/*,
 * which is the direction the layering rule mandates.
 */
export function AppHeader({
  onToggleNav,
  rightSlot,
}: {
  onToggleNav: () => void
  rightSlot?: React.ReactNode
}) {
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

      {rightSlot}
    </header>
  )
}
