import { createRootRouteWithContext, Outlet } from '@tanstack/react-router'
import type { store as appStore } from '@/store'
import { useAuthBootstrap } from '@/features/auth/use-auth-bootstrap'
import { NotFound } from '@/components/shared/not-found'

/**
 * Router context injected from `main.tsx`. Providing the store here lets route
 * `beforeLoad` guards read auth state without importing the store singleton
 * directly — a testable seam plus a cleaner one-way dependency.
 */
export interface RouterContext {
  store: typeof appStore
}

export const Route = createRootRouteWithContext<RouterContext>()({
  component: Root,
  notFoundComponent: NotFound,
})

function Root() {
  // Kick off the silent-refresh bootstrap as early as possible; the `/app`
  // route guard awaits the same singleton promise, so this just gives it a
  // head start instead of duplicating the request.
  useAuthBootstrap()
  return <Outlet />
}
