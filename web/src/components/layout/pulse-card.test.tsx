import { screen, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { WorkspacePulse } from '@/features/pulse/api'
import { PulseCard } from './pulse-card'

// The card renders router <Link>s; stub them to plain anchors. `search` is
// dropped the way an anchor would — the href assertion below covers the path.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, search: _search, children, ...props }: { to: string; search?: unknown; children: React.ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

const jsonHeaders = { 'content-type': 'application/json' }

/** A fully-healthy payload; tests override the slices they exercise. */
function healthyPulse(): WorkspacePulse {
  return {
    mailboxes: { total: 12, active: 12, paused: 0, error: 0 },
    warmup: { pool: 6, healthy: 6, watch: 0, at_risk: 0 },
    campaigns: { total: 8, running: 3, draft: 4, paused: 1 },
    contacts: { total: 1243 },
    sending: { sent_today: 247, daily_cap: 600 },
    inbox: { unread: 0, interested: 0 },
    attention: [],
  }
}

let pulseResponder: () => Response

beforeEach(() => {
  pulseResponder = () => new Response(JSON.stringify(healthyPulse()), { status: 200, headers: jsonHeaders })
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => pulseResponder()),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const authed = { auth: { status: 'authed' as const, activeWorkspaceId: 'ws-1' } }

test('quiet when healthy: zero attention rows collapse to the two-line form', async () => {
  renderWithProviders(<PulseCard />, { preloadedState: authed })

  expect(await screen.findByText(/all systems healthy/i)).toBeInTheDocument()
  // The send meter line stays; attention rows and the warmup line do not
  // (pool is 6, but the healthy form earns its quietness).
  expect(screen.getByText(/sending/i).closest('a')).toHaveAttribute('href', '/app/campaigns')
  expect(screen.getByText('247')).toBeInTheDocument()
  expect(screen.getByText('600')).toBeInTheDocument()
  expect(document.querySelector('[data-slot="pulse-attention-row"]')).toBeNull()
  expect(screen.queryByText(/warming/i)).not.toBeInTheDocument()
})

test('attention rows render worst-first (danger > warn > info), each linking to its href', async () => {
  const payload = healthyPulse()
  // Deliberately shuffled so the sort is what orders them. `daily_cap_near`
  // is intentionally NOT a known kind — it exercises the humanized fallback
  // for producers newer than this frontend.
  payload.attention = [
    { kind: 'daily_cap_near', severity: 'info', count: 1, reason: '90% consumed', href: '/app/campaigns' },
    { kind: 'mailbox_error', severity: 'danger', count: 2, reason: 'auth failed', href: '/app/mailboxes?status=error' },
    { kind: 'senders_gated', severity: 'warn', count: 3, reason: 'warming', href: '/app/warmup' },
  ]
  pulseResponder = () => new Response(JSON.stringify(payload), { status: 200, headers: jsonHeaders })

  renderWithProviders(<PulseCard />, { preloadedState: authed })

  await screen.findByText(/mailboxes need attention/i)
  const rows = Array.from(document.querySelectorAll('[data-slot="pulse-attention-row"]'))
  expect(rows).toHaveLength(3)
  expect(rows[0]).toHaveTextContent(/mailboxes need attention/i)
  expect(rows[1]).toHaveTextContent(/senders gated/i)
  expect(rows[2]).toHaveTextContent(/daily cap near/i)
  // Reason + destination ride along; the query string is split into `search`,
  // so the anchor carries the path.
  expect(rows[0]).toHaveTextContent('auth failed')
  expect(rows[0]).toHaveAttribute('href', '/app/mailboxes')
  // The warmup line appears in the attention form (pool > 0), warm-only.
  const warmupLine = screen.getByText(/warming · all healthy/i).closest('a')
  expect(warmupLine).toHaveAttribute('href', '/app/warmup')
})

// Every kind the server actually emits (internal/app/pulse/service.go) must
// map to operator copy — a renamed producer once fell through to the
// humanized-identifier fallback for 3 of 4 kinds without any test noticing.
test('every server attention kind renders its operator label, not the fallback', async () => {
  const payload = healthyPulse()
  payload.attention = [
    { kind: 'mailbox_error', severity: 'danger', count: 2, reason: 'auth failed', href: '/app/mailboxes?status=error' },
    { kind: 'senders_gated', severity: 'warn', count: 3, reason: '2 throttled, 1 paused', href: '/app/mailboxes' },
    { kind: 'dmarc_failing', severity: 'warn', count: 1, reason: 'no DMARC record', href: '/app/mailboxes' },
    { kind: 'cap_consumed', severity: 'info', count: 1, reason: 'daily cap 92% used', href: '/app/campaigns' },
  ]
  pulseResponder = () => new Response(JSON.stringify(payload), { status: 200, headers: jsonHeaders })

  renderWithProviders(<PulseCard />, { preloadedState: authed })

  expect(await screen.findByText(/mailboxes need attention/i)).toBeInTheDocument()
  expect(screen.getByText(/senders gated/i)).toBeInTheDocument()
  expect(screen.getByText(/domain failing DMARC/i)).toBeInTheDocument()
  expect(screen.getByText(/sending pool near daily cap/i)).toBeInTheDocument()
  // None fell through to the humanized `kind.replace` fallback.
  expect(screen.queryByText(/dmarc failing/i)).not.toBeInTheDocument()
  expect(screen.queryByText(/cap consumed/i)).not.toBeInTheDocument()
})

// The outcome line is the card's only answer to "is it working?", so it has
// to survive both postures — the healthy one and the one where an attention
// row is shouting. Its own zero state stays silent.
test('replies line renders in the healthy posture and links to the inbox', async () => {
  const payload = healthyPulse()
  payload.inbox = { unread: 23, interested: 6 }
  pulseResponder = () => new Response(JSON.stringify(payload), { status: 200, headers: jsonHeaders })

  renderWithProviders(<PulseCard />, { preloadedState: authed })

  await screen.findByText(/all systems healthy/i)
  const line = document.querySelector('[data-slot="pulse-replies-line"]')
  expect(line).toHaveAttribute('href', '/app/inbox')
  expect(line).toHaveTextContent('23 replies')
  expect(line).toHaveTextContent('6 interested')
})

test('replies line survives the attention posture — bad sending news does not hide good reply news', async () => {
  const payload = healthyPulse()
  payload.inbox = { unread: 4, interested: 0 }
  payload.attention = [
    { kind: 'mailbox_error', severity: 'danger', count: 2, reason: 'auth failed', href: '/app/mailboxes?status=error' },
  ]
  pulseResponder = () => new Response(JSON.stringify(payload), { status: 200, headers: jsonHeaders })

  renderWithProviders(<PulseCard />, { preloadedState: authed })

  await screen.findByText(/mailboxes need attention/i)
  const line = document.querySelector('[data-slot="pulse-replies-line"]')
  expect(line).toHaveTextContent('4 replies')
  // Zero interested is omitted rather than rendered as "0 interested".
  expect(line).not.toHaveTextContent(/interested/i)
})

test('replies line stays silent with no unread threads, and singularizes at one', async () => {
  renderWithProviders(<PulseCard />, { preloadedState: authed })
  await screen.findByText(/all systems healthy/i)
  expect(document.querySelector('[data-slot="pulse-replies-line"]')).toBeNull()

  const payload = healthyPulse()
  payload.inbox = { unread: 1, interested: 1 }
  pulseResponder = () => new Response(JSON.stringify(payload), { status: 200, headers: jsonHeaders })
  const { container } = renderWithProviders(<PulseCard />, { preloadedState: authed })

  await vi.waitFor(() => {
    const singular = container.querySelector('[data-slot="pulse-replies-line"]')
    expect(singular).toHaveTextContent('1 reply')
    // "reply", not "replies" — and the plural must not merely be a substring hit.
    expect(singular).not.toHaveTextContent(/replies/i)
  })
})

test('query error shows the danger body instead of stale-looking numbers', async () => {
  pulseResponder = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers: jsonHeaders })

  renderWithProviders(<PulseCard />, { preloadedState: authed })

  expect(await screen.findByText(/can't reach the server · retrying/i)).toBeInTheDocument()
  expect(screen.queryByText(/all systems healthy/i)).not.toBeInTheDocument()
  expect(screen.queryByText(/sending/i)).not.toBeInTheDocument()
})

test('loading reserves space with a skeleton, no numbers and no layout-jumping copy', () => {
  // Never resolves within the test — the card stays in its loading posture.
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))

  renderWithProviders(<PulseCard />, { preloadedState: authed })

  const card = screen.getByRole('region', { name: /workspace pulse/i })
  expect(within(card).queryByText(/all systems healthy/i)).not.toBeInTheDocument()
  expect(card.querySelectorAll('[data-slot="skeleton"]').length).toBeGreaterThan(0)
})
