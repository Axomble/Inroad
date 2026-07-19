import { createFileRoute, redirect, Outlet } from '@tanstack/react-router'
import { store } from '@/store'
import { runAuthBootstrap } from '@/features/auth/use-auth-bootstrap'
import { AppShell } from '@/components/layout/app-shell'

/**
 * Authenticated app layout. Guards every /app/* route: no in-memory session ->
 * redirect to the login screen. On a fresh page load the session hasn't been
 * restored yet (`status === 'idle'`), so the guard awaits the silent-refresh
 * bootstrap before deciding — this is what keeps an authenticated reload from
 * bouncing to `/` while the refresh request is still in flight. Renders the
 * shell (header + sidebar) around the routed content.
 */
export const Route = createFileRoute('/app')({
  beforeLoad: async () => {
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
  return (
    <AppShell>
      <Outlet />
    </AppShell>
  )
}
