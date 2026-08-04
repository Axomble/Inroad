import { Menu, Search, Sparkles } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { WorkspacePulse } from '@/features/pulse/api'
import { usePulseSelect } from './use-pulse'

/**
 * App shell header. Deliberately feature-agnostic — the workspace switcher and
 * account menu live in `features/auth/auth-header.tsx` and are passed in via
 * the `rightSlot` prop. Layout components must not import feature *UI*, which
 * is the direction the layering rule mandates; the pulse read here goes
 * through the `usePulse` hook seam (the `useNavCounts` doctrine).
 */
const selectNeedsAttention = (data: WorkspacePulse | undefined) => ({
  needsAttention: data?.attention.some((row) => row.severity === 'danger') ?? false,
})

const noop = () => undefined

export function AppHeader({
  navOpen,
  onToggleNav,
  onOpenPalette,
  agentOpen = false,
  onToggleAgent = noop,
  rightSlot,
}: {
  onToggleNav: () => void
  onOpenPalette: () => void
  agentOpen?: boolean
  onToggleAgent?: () => void
  navOpen: boolean
  rightSlot?: React.ReactNode
}) {
  // Below md the pulse card lives inside the closed drawer, so a danger row
  // would be invisible — the menu button carries a small danger dot (and says
  // so in its accessible name) to surface it.
  const { needsAttention } = usePulseSelect(selectNeedsAttention)

  return (
    <header className="flex h-14 shrink-0 items-center gap-3 border-b border-chrome-border bg-chrome px-3 text-chrome-text sm:px-4">
      <Button
        variant="ghost"
        size="icon-sm"
        className="relative text-chrome-muted hover:bg-chrome-hover hover:text-chrome-text md:hidden"
        onClick={onToggleNav}
        aria-label={needsAttention ? 'Toggle navigation — attention needed' : 'Toggle navigation'}
        aria-expanded={navOpen}
        aria-controls="mobile-navigation"
      >
        <Menu />
        {needsAttention && (
          <span className="absolute right-1 top-1 size-1.5 rounded-full bg-danger" aria-hidden="true" />
        )}
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

      <Button
        variant="ghost"
        size="sm"
        className="ml-auto text-chrome-muted hover:bg-chrome-hover hover:text-chrome-text"
        onClick={onToggleAgent}
        aria-label={agentOpen ? 'Close Inroad assistant' : 'Open Inroad assistant'}
        aria-expanded={agentOpen}
      >
        <Sparkles className="size-4 text-primary" aria-hidden="true" />
        <span className="hidden lg:inline">Assistant</span>
        <kbd className="hidden rounded border border-chrome-border px-1 py-0.5 font-mono text-[8px] text-chrome-muted sm:inline">@</kbd>
      </Button>

      {rightSlot}
    </header>
  )
}
