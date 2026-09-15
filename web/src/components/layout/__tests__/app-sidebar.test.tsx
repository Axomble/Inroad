import { fireEvent, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { config } from '@/lib/config'
import { AppSidebar } from '../app-sidebar'

// The sidebar renders router <Link>s; stub them to plain anchors so we can
// assert on the rendered nav without a real router. `activeOptions` is surfaced
// as a data attribute rather than dropped: whether a row matches its path
// exactly or by prefix decides which row lights up, so it is behavior worth
// pinning (see the exact-matching test below).
vi.mock('@tanstack/react-router', () => ({
  Link: ({
    to,
    children,
    activeProps: _activeProps,
    activeOptions,
    ...props
  }: {
    to: string
    children: React.ReactNode
    activeProps?: unknown
    activeOptions?: { exact?: boolean }
  }) => (
    <a href={to} data-active-exact={String(activeOptions?.exact ?? false)} {...props}>
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

const authed = { preloadedState: { auth: { role: 'member' as const, status: 'authed' as const } } }

test('the daily screens are flat, top-level rows', () => {
  renderWithProviders(<AppSidebar />, authed)
  expect(screen.getByRole('link', { name: /inbox/i })).toHaveAttribute('href', '/app/inbox')
  expect(screen.getByRole('link', { name: /campaigns/i })).toHaveAttribute('href', '/app/campaigns')
  expect(screen.getByRole('link', { name: /contacts/i })).toHaveAttribute('href', '/app/contacts')
  expect(screen.getByRole('link', { name: /mailboxes/i })).toHaveAttribute('href', '/app/mailboxes')
  expect(screen.getByRole('link', { name: /warmup/i })).toHaveAttribute('href', '/app/warmup')
  expect(screen.getByRole('link', { name: /reports/i })).toHaveAttribute('href', '/app/reports')
  expect(screen.getByRole('link', { name: /^settings$/i })).toHaveAttribute('href', '/app/settings')
})

// `/app` is the prefix of every screen in the app, so with TanStack's default
// prefix matching the Overview row lit up on Campaigns, Deals, Settings —
// everywhere — and two rows always looked selected at once. Section rows must
// keep prefix matching, though: Campaigns stays lit on a campaign's detail page.
test('Overview highlights only on its own page; section rows still match their subpages', () => {
  renderWithProviders(<AppSidebar />, authed)

  expect(screen.getByRole('link', { name: /overview/i })).toHaveAttribute('data-active-exact', 'true')
  for (const row of [/campaigns/i, /inbox/i, /contacts/i, /mailboxes/i, /warmup/i, /reports/i, /^settings$/i]) {
    expect(screen.getByRole('link', { name: row })).toHaveAttribute('data-active-exact', 'false')
  }
})

test('no group eyebrows: the old five-section nav does not come back', () => {
  renderWithProviders(<AppSidebar />, authed)
  // These were the section labels of the grouped nav; a flat rail has none.
  for (const label of ['Sending', 'Outreach', 'CRM', 'Workspace']) {
    expect(screen.queryByText(label)).not.toBeInTheDocument()
  }
})

test('secondary screens stay reachable behind the collapsed More row', () => {
  renderWithProviders(<AppSidebar />, authed)

  // Collapsed by default — the quiet screens don't crowd the rail.
  expect(screen.queryByRole('link', { name: /deliverability/i })).not.toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /companies/i })).not.toBeInTheDocument()

  const more = screen.getByRole('button', { name: /more/i })
  expect(more).toHaveAttribute('aria-expanded', 'false')
  fireEvent.click(more)
  expect(more).toHaveAttribute('aria-expanded', 'true')

  expect(screen.getByRole('link', { name: /approvals/i })).toHaveAttribute('href', '/app/approvals')
  expect(screen.getByRole('link', { name: /outbox/i })).toHaveAttribute('href', '/app/outbox')
  expect(screen.getByRole('link', { name: /deliverability/i })).toHaveAttribute('href', '/app/deliverability')
  expect(screen.getByRole('link', { name: /companies/i })).toHaveAttribute('href', '/app/companies')
  expect(screen.getByRole('link', { name: /deals/i })).toHaveAttribute('href', '/app/deals')
  // Docs are the external Starlight site, opened in a new tab — never an SPA route.
  const docsLink = screen.getByRole('link', { name: /docs & mcp/i })
  expect(docsLink).toHaveAttribute('href', config.docsUrl)
  expect(docsLink).toHaveAttribute('target', '_blank')
})

test('lists Deals exactly once, and no longer offers a row called CRM', () => {
  renderWithProviders(<AppSidebar />, authed)
  fireEvent.click(screen.getByRole('button', { name: /more/i }))
  // The old nav had a Deals row *and* a "CRM" row whose page opened on a deals
  // tab. Two ways to the same records is the bug this structure keeps fixed.
  expect(screen.getAllByRole('link', { name: /deals/i })).toHaveLength(1)
  expect(screen.queryByRole('link', { name: /^CRM$/ })).not.toBeInTheDocument()
})

// The seven settings screens live on the settings rail, one level down. The
// primary nav must guarantee it never carries the leaves — for any role, so an
// owner doesn't get an eight-row settings block back.
test.each(['member', 'admin', 'owner'])('Settings is one row with no leaves for a %s', (role) => {
  renderWithProviders(<AppSidebar />, {
    preloadedState: { auth: { role, status: 'authed' } },
  })

  expect(screen.getByRole('link', { name: /^settings$/i })).toHaveAttribute('href', '/app/settings')
  fireEvent.click(screen.getByRole('button', { name: /more/i }))
  for (const leaf of [/api keys/i, /connected apps/i, /reply labels/i, /custom fields/i, /^security$/i, /^team$/i]) {
    expect(screen.queryByRole('link', { name: leaf })).not.toBeInTheDocument()
  }
})
