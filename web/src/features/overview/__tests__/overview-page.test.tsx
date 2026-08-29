import { screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import type { WorkspacePulse } from '@/features/pulse/api'
import { OverviewPage } from '../overview-page'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, params: _params, search: _search, ...props }: { to: string; children: React.ReactNode; params?: unknown; search?: unknown }) => (
    <a href={to} {...props}>{children}</a>
  ),
}))

const headers = { 'content-type': 'application/json' }

/** The aggregate read-model the page now derives every count from. */
function pulsePayload(): WorkspacePulse {
  return {
    mailboxes: { total: 12, active: 9, paused: 2, error: 1 },
    warmup: { pool: 6, unknown: 0, healthy: 5, watch: 1, at_risk: 0, probation: 0, quarantine: 0 },
    campaigns: { total: 8, running: 3, draft: 4, paused: 1 },
    contacts: { total: 1243 },
    sending: { sent_today: 118, daily_cap: 640 },
    inbox: { unread: 0, interested: 0 },
    attention: [],
  }
}

let pulseResponder: () => Response
let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  pulseResponder = () => new Response(JSON.stringify(pulsePayload()), { status: 200, headers })
  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    if (url.includes('/pulse')) return pulseResponder()
    if (url.includes('/sending-domains')) {
      // A verified domain, so the setup checklist derives complete and stays
      // out of these assertions (its own file covers it).
      return new Response(JSON.stringify([{ domain: 'example.com', state: 'passing' }]), { status: 200, headers })
    }
    if (url.includes('/campaigns')) {
      return new Response(JSON.stringify([
        { id: 'c-1', name: 'Founder outreach', subject: 'A quick idea', status: 'running', stats: { sent: 57 } },
      ]), { status: 200, headers })
    }
    return new Response(JSON.stringify({ error: 'unhandled' }), { status: 404, headers })
  })
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const authed = {
  auth: { userName: 'Ava Stone', status: 'authed' as const, activeWorkspaceId: 'ws-1' },
}

function requestedUrls() {
  return fetchMock.mock.calls.map(([input]) => {
    const req = input as RequestInfo | URL
    return typeof req === 'string' ? req : req instanceof URL ? req.href : req.url
  })
}

test('the metric tiles render pulse aggregates, and daily capacity is the meter denominator', async () => {
  renderWithProviders(<OverviewPage />, { preloadedState: authed })

  expect(await screen.findByText('Good to see you, Ava.')).toBeInTheDocument()
  // Active mailboxes: pulse.mailboxes.active over total.
  expect(await screen.findByText('9')).toBeInTheDocument()
  expect(screen.getByText('12 connected')).toBeInTheDocument()
  // Daily capacity: pulse.sending.daily_cap — the SAME number the sidebar
  // meter uses as its denominator, ending the second client-side computation.
  expect(screen.getByText('640')).toBeInTheDocument()
  expect(screen.getByText('118 sent today')).toBeInTheDocument()
  // Live campaigns + drafts from pulse.campaigns.
  expect(screen.getByText('3')).toBeInTheDocument()
  expect(screen.getByText('4 drafts ready to refine')).toBeInTheDocument()
  // Warmup healthy over pool, and the derived health ring share.
  expect(screen.getByText('6 enrolled')).toBeInTheDocument()
  expect(screen.getByText('83%')).toBeInTheDocument()
  // Campaign rows still come from the one surviving list query.
  expect(screen.getByText('Founder outreach')).toBeInTheDocument()
  expect(screen.getByText('57')).toBeInTheDocument()
})

test('count-only list endpoints are no longer fetched; only pulse and the campaign rows are', async () => {
  renderWithProviders(<OverviewPage />, { preloadedState: authed })
  await screen.findByText('640')

  await waitFor(() => {
    const urls = requestedUrls()
    expect(urls.some((u) => u.includes('/pulse'))).toBe(true)
    expect(urls.some((u) => u.includes('/campaigns'))).toBe(true)
    // The old aggregate sources: a full mailbox list, the warmup overview and
    // the contact lists were downloaded just to print four numbers.
    expect(urls.some((u) => /\/mailboxes(\?|$)/.test(u))).toBe(false)
    expect(urls.some((u) => u.includes('/warmup/overview'))).toBe(false)
    expect(urls.some((u) => u.includes('/lists'))).toBe(false)
    // Exactly ONE pulse fetch: every consumer on the page (tiles, attention
    // panel, checklist) must ride the chrome's shared subscription — a second
    // request here means someone forked the poll with divergent options.
    expect(urls.filter((u) => u.includes('/pulse'))).toHaveLength(1)
  })
})

test('the attention panel renders server-defined pulse.attention rows, worst-first', async () => {
  const payload = pulsePayload()
  payload.attention = [
    { kind: 'senders_gated', severity: 'warn', count: 3, reason: '2 throttled, 1 paused', href: '/app/warmup' },
    { kind: 'mailbox_error', severity: 'danger', count: 2, reason: 'auth failed', href: '/app/mailboxes?status=error' },
  ]
  pulseResponder = () => new Response(JSON.stringify(payload), { status: 200, headers })

  renderWithProviders(<OverviewPage />, { preloadedState: authed })

  const danger = await screen.findByText(/mailboxes need attention/i)
  expect(danger.closest('a')).toHaveAttribute('href', '/app/mailboxes')
  expect(screen.getByText('auth failed')).toBeInTheDocument()
  const rows = screen.getByText('Needs attention').closest('section')?.querySelectorAll('li')
  expect(rows).toHaveLength(2)
  expect(rows?.[0]).toHaveTextContent(/mailboxes need attention/i)
  expect(rows?.[1]).toHaveTextContent(/senders gated/i)
  expect(screen.queryByText('Nothing urgent')).not.toBeInTheDocument()
})

test('a healthy pulse with no attention rows shows the clear state', async () => {
  renderWithProviders(<OverviewPage />, { preloadedState: authed })
  expect(await screen.findByText('Nothing urgent')).toBeInTheDocument()
})

test('a failing pulse query surfaces the shared metrics banner', async () => {
  pulseResponder = () => new Response(JSON.stringify({ error: 'boom' }), { status: 500, headers })

  renderWithProviders(<OverviewPage />, { preloadedState: authed })

  expect(await screen.findByText(/some live metrics could not be loaded/i)).toBeInTheDocument()
  // Campaign rows are an independent query and still render.
  expect(await screen.findByText('Founder outreach')).toBeInTheDocument()
})
