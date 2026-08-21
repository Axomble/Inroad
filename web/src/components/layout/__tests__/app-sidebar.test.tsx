import { screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { config } from '@/lib/config'
import { AppSidebar } from '../app-sidebar'

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

test('groups the three CRM record types under one CRM heading', () => {
  renderWithProviders(<AppSidebar />, {
    preloadedState: { auth: { role: 'member', status: 'authed' } },
  })
  expect(screen.getByText('CRM')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /contacts/i })).toHaveAttribute('href', '/app/contacts')
  expect(screen.getByRole('link', { name: /companies/i })).toHaveAttribute('href', '/app/companies')
  expect(screen.getByRole('link', { name: /deals/i })).toHaveAttribute('href', '/app/deals')
  // Campaigns is outbound, not a CRM record type, so it stays under Outreach.
  expect(screen.getByRole('link', { name: /campaigns/i })).toHaveAttribute('href', '/app/campaigns')
})

test('lists Deals exactly once, and no longer offers a row called CRM', () => {
  renderWithProviders(<AppSidebar />, {
    preloadedState: { auth: { role: 'member', status: 'authed' } },
  })
  // The old nav had a Deals row *and* a "CRM" row whose page opened on a deals
  // tab. Two ways to the same records is the bug this grouping fixes.
  expect(screen.getAllByRole('link', { name: /deals/i })).toHaveLength(1)
  expect(screen.queryByRole('link', { name: /^CRM$/ })).not.toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /revenue workspace/i })).not.toBeInTheDocument()
})

// The seven settings screens now live on the settings rail, one level down.
// Role-gating moved there with them (see settings-rail.test.tsx); what the
// primary nav must guarantee is that it no longer carries the leaves at all —
// for any role, so an owner doesn't get the old eight-row Workspace group back.
test.each(['member', 'admin', 'owner'])('the Workspace group is one Settings row for a %s', (role) => {
  renderWithProviders(<AppSidebar />, {
    preloadedState: { auth: { role, status: 'authed' } },
  })

  expect(screen.getByRole('link', { name: /^settings$/i })).toHaveAttribute('href', '/app/settings')
  // Docs are the external Starlight site, opened in a new tab — never an SPA route.
  const docsLink = screen.getByRole('link', { name: /docs & mcp/i })
  expect(docsLink).toHaveAttribute('href', config.docsUrl)
  expect(docsLink).toHaveAttribute('target', '_blank')
  for (const leaf of [/api keys/i, /connected apps/i, /reply labels/i, /custom fields/i, /^security$/i, /^team$/i]) {
    expect(screen.queryByRole('link', { name: leaf })).not.toBeInTheDocument()
  }
})
