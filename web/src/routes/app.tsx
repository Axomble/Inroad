import { createFileRoute, redirect, Outlet } from '@tanstack/react-router'
import { runAuthBootstrap } from '@/features/auth/use-auth-bootstrap'
import { AppShell } from '@/components/layout/app-shell'
import { AuthHeader } from '@/features/auth/auth-header'
import { useAuthGuard } from '@/features/auth/use-auth-guard'
import { UnverifiedBanner } from '@/features/auth/unverified-banner'

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
      <UnverifiedBanner />
      {/* AppShell fills whatever height remains below the banner (h-full,
          not h-dvh — this wrapper owns the viewport height so the banner
          can take its own space above the shell without either overflowing
          or fighting AppShell's internal flex layout). */}
      <div className="min-h-0 flex-1">
        <AppShell rightSlot={<AuthHeader />}>
          <Outlet />
        </AppShell>
      </div>
    </div>
  )
}
