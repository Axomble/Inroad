import { Suspense, lazy, useState } from 'react'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import { toggleSidebar } from '@/store/slices/ui'
import { cn } from '@/lib/utils'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useHotkey } from '@/hooks/use-hotkey'
import { AppHeader } from './app-header'
import { AppSidebar } from './app-sidebar'

// Only needed once someone presses ⌘K, so it stays out of the initial bundle —
// same pattern the warmup sparkline uses. No Suspense fallback: a spinner that
// appears for one frame before the palette is worse than the palette simply
// appearing, and the chunk is a few KB.
const CommandPalette = lazy(() =>
  import('@/components/shared/command-palette').then((m) => ({ default: m.CommandPalette })),
)

/**
 * Authenticated app frame: header + sidebar over the chrome, with the
 * content surface reading as one continuous frame (rounded top-left where it
 * meets the chrome). Below md the sidebar collapses to a drawer; its open
 * state lives in the `ui` redux slice.
 *
 * Sizes to `h-full` rather than owning the viewport height itself — the
 * caller (`routes/app.tsx`) reserves `h-dvh` on an outer wrapper so it can
 * stack an unverified-email banner above the shell without either
 * overflowing the viewport or fighting this component's internal flex math.
 */
export function AppShell({
  children,
  rightSlot,
}: {
  children: React.ReactNode
  rightSlot?: React.ReactNode
}) {
  const open = useAppSelector((s) => s.ui.sidebarOpen)
  const dispatch = useAppDispatch()
  const close = () => {
    if (open) dispatch(toggleSidebar())
  }

  // Local state, not redux: nothing outside this subtree needs to know the
  // palette is open, and it must not be persisted across reloads.
  const [paletteOpen, setPaletteOpen] = useState(false)
  useHotkey({ key: 'k', mod: true, whileTyping: true }, () => setPaletteOpen(true))

  return (
    <TooltipProvider>
      <div className="flex h-full flex-col overflow-hidden bg-rail text-foreground">
        <AppHeader onToggleNav={() => dispatch(toggleSidebar())} rightSlot={rightSlot} />

        <div className="flex min-h-0 flex-1">
          {/* Desktop sidebar */}
          <div className="hidden shrink-0 md:block">
            <AppSidebar />
          </div>

          {/* Mobile drawer */}
          <div
            className={cn(
              'fixed inset-0 z-40 bg-background/60 backdrop-blur-sm transition-opacity md:hidden',
              open ? 'opacity-100' : 'pointer-events-none opacity-0',
            )}
            onClick={close}
            aria-hidden="true"
          />
          <div
            className={cn(
              'fixed inset-y-0 left-0 z-40 border-r border-border bg-rail transition-transform md:hidden',
              open ? 'translate-x-0' : '-translate-x-full',
            )}
          >
            <AppSidebar />
          </div>

          <main className="min-w-0 flex-1 overflow-hidden border-t border-border bg-background md:rounded-tl-2xl md:border-l">
            {children}
          </main>
        </div>

        {paletteOpen && (
          <Suspense fallback={null}>
            <CommandPalette onClose={() => setPaletteOpen(false)} />
          </Suspense>
        )}
      </div>
    </TooltipProvider>
  )
}
