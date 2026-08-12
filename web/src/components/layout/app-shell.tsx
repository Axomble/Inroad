import { Suspense, lazy, useEffect, useRef, useState } from 'react'
import { cn } from '@/lib/utils'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useHotkey } from '@/hooks/use-hotkey'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import { setAgentPanelOpen, toggleAgentPanel } from '@/store/slices/ui'
import { AppHeader } from './app-header'
import { AppSidebar } from './app-sidebar'
import { ToastHost } from './toast-host'

// Only needed once someone presses ⌘K, so it stays out of the initial bundle —
// same pattern the warmup sparkline uses. No Suspense fallback: a spinner that
// appears for one frame before the palette is worse than the palette simply
// appearing, and the chunk is a few KB.
const CommandPalette = lazy(() =>
  import('@/components/shared/command-palette').then((m) => ({ default: m.CommandPalette })),
)

const AgentPanel = lazy(() =>
  import('@/features/agent/panel').then((module) => ({ default: module.AgentPanel })),
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
  const [navOpen, setNavOpen] = useState(false)
  const dispatch = useAppDispatch()
  const agentOpen = useAppSelector((state) => state.ui.agentPanelOpen)
  const [agentLoaded, setAgentLoaded] = useState(
    () => agentOpen || new URL(window.location.href).searchParams.has('thread'),
  )
  const agentOpenerRef = useRef<HTMLElement | null>(null)
  const agentWasOpenRef = useRef(agentOpen)
  const close = () => {
    setNavOpen(false)
  }

  // Local state, not redux: nothing outside this subtree needs to know the
  // palette is open, and it must not be persisted across reloads.
  const [paletteOpen, setPaletteOpen] = useState(false)
  const mobileNavRef = useRef<HTMLDivElement>(null)
  // The element focus came from, so dismissing the drawer returns the caret to
  // the toggle rather than dropping it at the top of the document.
  const navOpenerRef = useRef<HTMLElement | null>(null)
  useHotkey({ key: 'Escape' }, close, navOpen)
  useEffect(() => {
    if (navOpen) {
      navOpenerRef.current = document.activeElement as HTMLElement | null
      mobileNavRef.current?.querySelector<HTMLElement>('a, button')?.focus()
      return
    }
    navOpenerRef.current?.focus()
    navOpenerRef.current = null
  }, [navOpen])

  useHotkey({ key: 'k', mod: true, whileTyping: true }, () => setPaletteOpen(true))
  const toggleAgent = () => {
    if (!agentOpen) agentOpenerRef.current = document.activeElement as HTMLElement | null
    setAgentLoaded(true)
    dispatch(toggleAgentPanel())
  }

  useHotkey({ key: '@', shift: 'any' }, toggleAgent)
  // No `whileTyping`: that flag also switches off the guard that keeps hotkeys
  // out of dialogs, menus and text fields, so Escape closing a modal — or
  // cancelling an inline rename inside the panel — would close the panel too.
  useHotkey({ key: 'Escape' }, () => dispatch(setAgentPanelOpen(false)), agentOpen)

  const openAgent = () => {
    if (!agentOpen) agentOpenerRef.current = document.activeElement as HTMLElement | null
    setAgentLoaded(true)
    dispatch(setAgentPanelOpen(true))
  }

  useEffect(() => {
    if (!agentOpen && agentWasOpenRef.current) {
      agentOpenerRef.current?.focus()
      agentOpenerRef.current = null
    }
    agentWasOpenRef.current = agentOpen
  }, [agentOpen])

  return (
    <TooltipProvider>
      <div className="flex h-full flex-col overflow-hidden bg-rail text-foreground">
        <AppHeader
          navOpen={navOpen}
          onToggleNav={() => setNavOpen((value) => !value)}
          onOpenPalette={() => setPaletteOpen(true)}
          agentOpen={agentOpen}
          onToggleAgent={toggleAgent}
          rightSlot={rightSlot}
        />

        <div className="flex min-h-0 flex-1">
          {/* Desktop sidebar */}
          <div className="hidden shrink-0 md:block">
            <AppSidebar onOpenAgent={openAgent} />
          </div>

          {/* Mobile drawer */}
          <div
            className={cn(
              'fixed inset-0 z-40 bg-background/60 backdrop-blur-sm transition-opacity md:hidden',
              navOpen ? 'opacity-100' : 'pointer-events-none opacity-0',
            )}
            onClick={close}
            aria-hidden="true"
          />
          <div
            ref={mobileNavRef}
            id="mobile-navigation"
            role="dialog"
            aria-modal="true"
            aria-label="Primary navigation"
            className={cn(
              'fixed inset-y-0 left-0 z-40 border-r border-border bg-rail transition-transform md:hidden',
              navOpen ? 'translate-x-0' : '-translate-x-full',
            )}
            // `inert`, not `aria-hidden`: when closed the drawer is only
            // translated off-screen, so its links stay tabbable. aria-hidden
            // over focusable content is the violation; inert removes the
            // subtree from both the tab order and the accessibility tree.
            inert={!navOpen}
            onClick={(event) => {
              if ((event.target as HTMLElement).closest('a')) close()
            }}
          >
            <AppSidebar onOpenAgent={() => {
              close()
              openAgent()
            }} />
          </div>

          <main className="workspace-grid min-w-0 flex-1 overflow-hidden bg-background md:rounded-tl-[22px] md:border-l md:border-t md:border-chrome-border">
            {children}
          </main>
          {agentLoaded && (
            <Suspense fallback={null}>
              <AgentPanel />
            </Suspense>
          )}
        </div>

        {paletteOpen && (
          <Suspense fallback={null}>
            <CommandPalette onClose={() => setPaletteOpen(false)} onOpenAgent={openAgent} />
          </Suspense>
        )}

        {/* Mounted once for the whole authenticated app: a toast outlives the
            screen that raised it, which is the entire point of it not being a
            page-level banner. Fixed-positioned, so it costs no layout here. */}
        <ToastHost />
      </div>
    </TooltipProvider>
  )
}
