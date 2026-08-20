import { screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { WorkspacePulse } from '@/features/pulse/api'
import { AppHeader } from '../app-header'

// The header renders a router <Link>; stub it to a plain anchor so we can
// assert on the rendered header without a real router.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, activeProps: _activeProps, ...props }: { to: string; children: React.ReactNode; activeProps?: unknown }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

const jsonHeaders = { 'content-type': 'application/json' }

function pulseWith(attention: WorkspacePulse['attention']): WorkspacePulse {
  return {
    mailboxes: { total: 1, active: 1, paused: 0, error: 0 },
    warmup: { pool: 0, unknown: 0, healthy: 0, watch: 0, at_risk: 0, probation: 0, quarantine: 0 },
    campaigns: { total: 0, running: 0, draft: 0, paused: 0 },
    contacts: { total: 0 },
    sending: { sent_today: 0, daily_cap: 0 },
    inbox: { unread: 0, interested: 0 },
    attention,
  }
}

let pulseResponder: () => Response
let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  pulseResponder = () => new Response(JSON.stringify(pulseWith([])), { status: 200, headers: jsonHeaders })
  fetchMock = vi.fn(async () => pulseResponder())
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const authed = { auth: { status: 'authed' as const, activeWorkspaceId: 'ws-1' } }
const noop = () => {}

function renderHeader() {
  return renderWithProviders(<AppHeader navOpen={false} onToggleNav={noop} onOpenPalette={noop} />, {
    preloadedState: authed,
  })
}

test('a danger attention row puts the dot on the menu button and says so in its name', async () => {
  pulseResponder = () =>
    new Response(
      JSON.stringify(
        pulseWith([{ kind: 'mailbox_error', severity: 'danger', count: 2, reason: 'auth failed', href: '/app/mailboxes' }]),
      ),
      { status: 200, headers: jsonHeaders },
    )

  renderHeader()

  const button = await screen.findByRole('button', { name: 'Toggle navigation — attention needed' })
  // The dot is decorative (aria-hidden) — the accessible name carries the state.
  expect(button.querySelector('.bg-danger')).not.toBeNull()
})

test('warn/info attention does not raise the dot — danger only', async () => {
  pulseResponder = () =>
    new Response(
      JSON.stringify(
        pulseWith([{ kind: 'sender_gated', severity: 'warn', count: 1, reason: 'warming', href: '/app/warmup' }]),
      ),
      { status: 200, headers: jsonHeaders },
    )

  renderHeader()

  // Let the pulse payload actually land, then confirm the name never changed —
  // asserting before the fetch settles would pass vacuously.
  await waitFor(() => expect(fetchMock).toHaveBeenCalled())
  const button = await screen.findByRole('button', { name: 'Toggle navigation' })
  await waitFor(() => expect(screen.queryByRole('button', { name: /attention needed/ })).not.toBeInTheDocument())
  expect(button.querySelector('.bg-danger')).toBeNull()
})
