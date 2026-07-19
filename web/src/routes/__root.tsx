import { createRootRoute, Outlet } from '@tanstack/react-router'
import { useAuthBootstrap } from '@/features/auth/use-auth-bootstrap'

export const Route = createRootRoute({
  component: Root,
})

function Root() {
  // Kick off the silent-refresh bootstrap as early as possible; the `/app`
  // route guard awaits the same singleton promise, so this just gives it a
  // head start instead of duplicating the request.
  useAuthBootstrap()
  return <Outlet />
}
