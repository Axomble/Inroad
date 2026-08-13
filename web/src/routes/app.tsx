import { createFileRoute, redirect, Outlet } from '@tanstack/react-router'
import { runAuthBootstrap } from '@/features/auth/use-auth-bootstrap'
import { AppShell } from '@/components/layout/app-shell'
import { AuthHeader } from '@/features/auth/auth-header'
import { WorkspaceSwitcher } from '@/features/auth/workspace-switcher'
import { useAuthGuard } from '@/features/auth/use-auth-guard'
import { UnverifiedBanner } from '@/features/auth/unverified-banner'
import { WorkspaceOnboardingOverlay } from '@/features/auth/workspace-onboarding-overlay'

/**
 * Authenticated app layout. Guards every /app/* route: no in-memory session ->
 * redirect to the login screen. On a fresh page load the session hasn't been
 * restored yet (`status === 'idle'`), so the guard awaits the silent-refresh
 * bootstrap before deciding — this is what keeps an authenticated reload from
 * bouncing to `/` while the refresh request is still in flight. Renders the
 * shell (header + sidebar) around the routed content.
 *
 * The store is pulled from router context rather than imported directly so
 * this module stays testable — a test can `createRouter` with any store shape.
 */
export const Route = createFileRoute('/app')({
  // `thread` is declared on the layout, not on each page, because the
  // assistant panel lives in the shell and must keep its deep link while the
  // user moves between /app/* pages — a param missing from every matched
  // route's validator is stripped on the next navigation.
  validateSearch: (search: Record<string, unknown>): { thread?: string } =>
    typeof search.thread === 'string' && search.thread ? { thread: search.thread } : {},
  beforeLoad: async ({ context }) => {
    const { store } = context
    if (store.getState().auth.status === 'idle') {
      await runAuthBootstrap(store.dispatch)
    }
    if (!store.getState().auth.accessToken) {
      throw redirect({ to: '/' })
    }
  },
  component: AppLayout,
})

function AppLayout() {
  // Watch the in-memory access token: if it clears mid-session (a background
  // reauth failure, for example) the user goes back to the login page instead
  // of staring at a broken-looking app shell.
  useAuthGuard()
  return (
    <div className="flex h-dvh flex-col">
      {/* Sits alongside the unverified banner rather than inside a page: an
          un-named workspace blocks every /app route, not one of them. Renders
          nothing (and costs nothing) once the workspace has been named. */}
      <WorkspaceOnboardingOverlay />
      <UnverifiedBanner />
      {/* AppShell fills whatever height remains below the banner (h-full,
          not h-dvh — this wrapper owns the viewport height so the banner
          can take its own space above the shell without either overflowing
          or fighting AppShell's internal flex layout). */}
      <div className="min-h-0 flex-1">
        <AppShell
          leftSlot={
            // Workspace identity sits beside the product mark, separated by a
            // hairline; utilities (docs, assistant, theme, account) keep right.
            <div className="hidden items-center border-l border-chrome-border pl-3 sm:flex">
              <WorkspaceSwitcher />
            </div>
          }
          rightSlot={<AuthHeader />}
        >
          <Outlet />
        </AppShell>
      </div>
    </div>
  )
}
