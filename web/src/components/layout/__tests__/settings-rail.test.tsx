import { screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { SettingsRail } from '../settings-rail'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, activeProps: _activeProps, ...props }: { to: string; children: React.ReactNode; activeProps?: unknown }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

beforeEach(() => {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify([]), { status: 200, headers: { 'content-type': 'application/json' } })),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

// Security is personal account config; reply-label and custom-field writes are
// campaigns:write / contacts:write, which every member holds.
const MEMBER_ROWS = [
  [/^security$/i, '/app/settings/security'],
  [/reply labels/i, '/app/settings/reply-labels'],
  [/custom fields/i, '/app/settings/custom-fields'],
] as const

// Each of these sits behind RequireRole("admin") on the server. Team is here
// rather than above because every /workspaces/{id}/invites route is admin-only
// — the old sidebar advertised it to members anyway, who reached an
// "Admins only" wall.
const ADMIN_ROWS = [
  [/^team$/i, '/app/settings/team'],
  [/api keys/i, '/app/settings/api-keys'],
  [/connected apps/i, '/app/settings/oauth-apps'],
  [/^ai$/i, '/app/settings/ai'],
] as const

test('a member sees the screens their scopes actually allow, and none of the admin ones', () => {
  renderWithProviders(<SettingsRail />, { preloadedState: { auth: { role: 'member', status: 'authed' } } })

  for (const [name, href] of MEMBER_ROWS) {
    expect(screen.getByRole('link', { name })).toHaveAttribute('href', href)
  }
  for (const [name] of ADMIN_ROWS) {
    expect(screen.queryByRole('link', { name })).not.toBeInTheDocument()
  }
})

// Owner outranks admin, so it must clear an admin minimum too — the bug a
// hardcoded `role === 'admin'` check would introduce.
test.each(['admin', 'owner'])('a %s sees every settings screen', (role) => {
  renderWithProviders(<SettingsRail />, { preloadedState: { auth: { role, status: 'authed' } } })

  for (const [name, href] of [...MEMBER_ROWS, ...ADMIN_ROWS]) {
    expect(screen.getByRole('link', { name })).toHaveAttribute('href', href)
  }
})

// Fail-closed: before the session settles `role` is null, and a role from a
// newer server is unrecognized. Neither may reveal an admin-gated row.
test.each([null, 'superuser'])('an unresolved or unknown role (%s) sees no admin rows', (role) => {
  renderWithProviders(<SettingsRail />, { preloadedState: { auth: { role, status: 'authed' } } })

  for (const [name] of ADMIN_ROWS) {
    expect(screen.queryByRole('link', { name })).not.toBeInTheDocument()
  }
})
