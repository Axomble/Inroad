import { createFileRoute, redirect } from '@tanstack/react-router'

/**
 * /app/settings has no landing screen of its own — a settings index that only
 * repeats the rail's links would be a click with nothing behind it.
 *
 * Security is the target because it's the only settings screen every role can
 * fully use: it configures the signed-in user's own 2FA, passkeys and
 * sessions. Team looks like the friendlier default and isn't — its invite
 * routes are all `RequireRole("admin")`, so landing a member there would open
 * settings on an "Admins only" wall. A role-aware redirect would fix that too,
 * but it would also have to re-derive the rail's gating a second time; picking
 * the universally-usable screen needs no role logic at all.
 */
export const Route = createFileRoute('/app/settings/')({
  beforeLoad: () => {
    throw redirect({ to: '/app/settings/security' })
  },
})
