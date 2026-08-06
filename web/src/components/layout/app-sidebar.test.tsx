import { screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { AppSidebar } from './app-sidebar'

// The sidebar renders router <Link>s; stub them to plain anchors so we can
// assert on the rendered nav without a real router.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, activeProps: _activeProps, ...props }: { to: string; children: React.ReactNode; activeProps?: unknown }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

beforeEach(() => {
  // No activeWorkspaceId is preloaded, so the pulse query is skipped and no
  // counts render; the stub is a safety net so nothing rejects if a request
  // does fire.
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify([]), { status: 200, headers: { 'content-type': 'application/json' } })),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('shows the API keys nav item for an admin', () => {
  renderWithProviders(<AppSidebar />, {
    preloadedState: { auth: { role: 'admin', status: 'authed' } },
  })
  expect(screen.getByRole('link', { name: /api keys/i })).toBeInTheDocument()
})

test('shows the API keys nav item for an owner', () => {
  renderWithProviders(<AppSidebar />, {
    preloadedState: { auth: { role: 'owner', status: 'authed' } },
  })
  expect(screen.getByRole('link', { name: /api keys/i })).toBeInTheDocument()
})

test('the Inbox screen is reachable from the nav', () => {
  renderWithProviders(<AppSidebar />, {
    preloadedState: { auth: { role: 'member', status: 'authed' } },
  })
  expect(screen.getByRole('link', { name: /inbox/i })).toHaveAttribute('href', '/app/inbox')
})

test('the Deliverability screen is reachable from the nav, with no invented count', () => {
  renderWithProviders(<AppSidebar />, {
    preloadedState: { auth: { role: 'member', status: 'authed' } },
  })
  const link = screen.getByRole('link', { name: /deliverability/i })
  expect(link).toHaveAttribute('href', '/app/deliverability')
  // The score is not a count, and there is no cheap workspace-wide number to
  // put here — so the row carries none rather than a made-up one.
  expect(link).toHaveTextContent(/^Deliverability$/)
})

test('hides the API keys nav item from a non-admin member', () => {
  renderWithProviders(<AppSidebar />, {
    preloadedState: { auth: { role: 'member', status: 'authed' } },
  })
  expect(screen.queryByRole('link', { name: /api keys/i })).not.toBeInTheDocument()
  // The rest of the Workspace group still renders.
  expect(screen.getByRole('link', { name: /security/i })).toBeInTheDocument()
})
