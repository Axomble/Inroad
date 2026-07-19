import { createFileRoute, redirect, Outlet } from '@tanstack/react-router'
import { store } from '@/store'
import { AppShell } from '@/components/layout/app-shell'

/**
 * Authenticated app layout. Guards every /app/* route: no session token ->
 * redirect to the login screen. Renders the shell (header + sidebar) around
 * the routed content.
 */
export const Route = createFileRoute('/app')({
  beforeLoad: () => {
    if (!store.getState().auth.token) {
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
